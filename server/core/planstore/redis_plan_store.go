// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package planstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	securejoin "github.com/cyphar/filepath-securejoin"
	"github.com/redis/go-redis/v9"

	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/logging"
	"github.com/runatlantis/atlantis/server/utils"
)

const redisOpTimeout = 30 * time.Second

// RedisPlanStore implements PlanStore by persisting plan files as redis hashes,
// with a per-pull index set so listing and bulk deletion avoid a keyspace scan.
type RedisPlanStore struct {
	client redis.Cmdable
	prefix string
	ttl    time.Duration
	logger logging.SimpleLogging
}

// NewRedisPlanStore builds a store over an existing redis handle. ttl of 0
// disables expiry, leaving cleanup to Remove and DeleteForPull.
func NewRedisPlanStore(client redis.Cmdable, prefix string, ttl time.Duration, logger logging.SimpleLogging) *RedisPlanStore {
	return &RedisPlanStore{
		client: client,
		prefix: strings.TrimSuffix(prefix, ":"),
		ttl:    ttl,
		logger: logger,
	}
}

// pullTag co-locates a pull's index set and every plan hash in one cluster slot,
// which TxPipeline and multi-key DEL require.
func (s *RedisPlanStore) pullTag(owner, repo string, pullNum int) string {
	return fmt.Sprintf("{%s/%s/%d}", owner, repo, pullNum)
}

func (s *RedisPlanStore) join(parts ...string) string {
	if s.prefix != "" {
		parts = append([]string{s.prefix}, parts...)
	}
	return strings.Join(parts, ":")
}

func (s *RedisPlanStore) indexKey(owner, repo string, pullNum int) string {
	return s.join("planidx", s.pullTag(owner, repo, pullNum))
}

func (s *RedisPlanStore) objKey(owner, repo string, pullNum int, member string) string {
	return s.join("plan", s.pullTag(owner, repo, pullNum), member)
}

// planMember is the index-set member and the restore-relative path below the
// pull: workspace/repoRelDir/planfile.
func planMember(workspace, repoRelDir, planFile string) string {
	return strings.Join([]string{workspace, repoRelDir, planFile}, "/")
}

func (s *RedisPlanStore) Save(ctx command.ProjectContext, planPath string) error {
	data, err := os.ReadFile(planPath)
	if err != nil {
		return fmt.Errorf("reading plan file for redis save: %w", err)
	}
	member := planMember(ctx.Workspace, ctx.RepoRelDir, filepath.Base(planPath))
	obj := s.objKey(ctx.BaseRepo.Owner, ctx.BaseRepo.Name, ctx.Pull.Num, member)
	idx := s.indexKey(ctx.BaseRepo.Owner, ctx.BaseRepo.Name, ctx.Pull.Num)

	rctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()
	pipe := s.client.TxPipeline()
	pipe.HSet(rctx, obj, map[string]any{
		"data":        data,
		"head-commit": ctx.Pull.HeadCommit,
		"planned-by":  ctx.User.Username,
	})
	pipe.SAdd(rctx, idx, member)
	if s.ttl > 0 {
		pipe.Expire(rctx, obj, s.ttl)
		pipe.Expire(rctx, idx, s.ttl)
	}
	if _, err := pipe.Exec(rctx); err != nil {
		return fmt.Errorf("saving plan to redis (key=%s): %w", obj, err)
	}
	s.logger.Info("saved plan to redis %s", obj)
	return nil
}

func (s *RedisPlanStore) Load(ctx command.ProjectContext, planPath string) error {
	obj := s.objKey(ctx.BaseRepo.Owner, ctx.BaseRepo.Name, ctx.Pull.Num,
		planMember(ctx.Workspace, ctx.RepoRelDir, filepath.Base(planPath)))

	rctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()
	vals, err := s.client.HMGet(rctx, obj, "data", "head-commit").Result()
	if err != nil {
		return fmt.Errorf("loading plan from redis (key=%s): %w", obj, err)
	}
	if vals[0] == nil {
		return fmt.Errorf("plan not found in redis (key=%s), run plan again", obj)
	}
	planCommit, _ := vals[1].(string)
	if planCommit == "" {
		return fmt.Errorf("plan in redis has no head-commit (key=%s), run plan again", obj)
	}
	if ctx.Pull.HeadCommit != "" && planCommit != ctx.Pull.HeadCommit {
		return fmt.Errorf("plan was created at commit %.8s but PR is now at %.8s, run plan again", planCommit, ctx.Pull.HeadCommit)
	}
	if err := os.MkdirAll(filepath.Dir(planPath), 0o700); err != nil {
		return fmt.Errorf("creating parent directories for plan file: %w", err)
	}
	return os.WriteFile(planPath, []byte(vals[0].(string)), 0o600)
}

func (s *RedisPlanStore) Remove(ctx command.ProjectContext, planPath string) error {
	member := planMember(ctx.Workspace, ctx.RepoRelDir, filepath.Base(planPath))
	rctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()
	pipe := s.client.TxPipeline()
	pipe.Del(rctx, s.objKey(ctx.BaseRepo.Owner, ctx.BaseRepo.Name, ctx.Pull.Num, member))
	pipe.SRem(rctx, s.indexKey(ctx.BaseRepo.Owner, ctx.BaseRepo.Name, ctx.Pull.Num), member)
	if _, err := pipe.Exec(rctx); err != nil {
		s.logger.Warn("failed to delete plan from redis: %v", err)
	}
	return utils.RemoveIgnoreNonExistent(planPath)
}

func (s *RedisPlanStore) members(owner, repo string, pullNum int) ([]string, error) {
	rctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()
	members, err := s.client.SMembers(rctx, s.indexKey(owner, repo, pullNum)).Result()
	if err != nil {
		return nil, fmt.Errorf("listing redis plan index for %s/%s#%d: %w", owner, repo, pullNum, err)
	}
	return members, nil
}

func (s *RedisPlanStore) ListWorkspaces(owner, repo string, pullNum int) ([]string, error) {
	members, err := s.members(owner, repo, pullNum)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, m := range members {
		if ws, _, ok := strings.Cut(m, "/"); ok && ws != "" {
			seen[ws] = struct{}{}
		}
	}
	workspaces := make([]string, 0, len(seen))
	for ws := range seen {
		workspaces = append(workspaces, ws)
	}
	return workspaces, nil
}

func (s *RedisPlanStore) RestorePlans(pullDir, owner, repo string, pullNum int) error {
	if pullDir == "" {
		return nil // capability probe: redis store supports restore
	}
	members, err := s.members(owner, repo, pullNum)
	if err != nil {
		return err
	}

	rctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()
	var restored int
	for _, m := range members {
		// SecureJoin keeps the write inside pullDir even if a member value is
		// tampered with in redis.
		localPath, err := securejoin.SecureJoin(pullDir, m)
		if err != nil {
			return fmt.Errorf("resolving safe path for redis member %s: %w", m, err)
		}
		data, err := s.client.HGet(rctx, s.objKey(owner, repo, pullNum, m), "data").Bytes()
		if err != nil {
			return fmt.Errorf("restoring plan from redis (member=%s): %w", m, err)
		}
		if err := os.MkdirAll(filepath.Dir(localPath), 0o700); err != nil {
			return fmt.Errorf("creating directory for restored plan: %w", err)
		}
		if err := os.WriteFile(localPath, data, 0o600); err != nil {
			return fmt.Errorf("writing restored plan file %s: %w", localPath, err)
		}
		restored++
	}
	s.logger.Info("restored %d plan(s) from redis for %s/%s#%d", restored, owner, repo, pullNum)
	return nil
}

func (s *RedisPlanStore) DeleteForPull(owner, repo string, pullNum int) error {
	members, err := s.members(owner, repo, pullNum)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(members)+1)
	for _, m := range members {
		keys = append(keys, s.objKey(owner, repo, pullNum, m))
	}
	keys = append(keys, s.indexKey(owner, repo, pullNum))

	rctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()
	if err := s.client.Del(rctx, keys...).Err(); err != nil {
		s.logger.Warn("failed to delete plans from redis for %s/%s#%d: %v", owner, repo, pullNum, err)
		return nil
	}
	if len(members) > 0 {
		s.logger.Info("deleted %d plan(s) from redis for %s/%s#%d", len(members), owner, repo, pullNum)
	}
	return nil
}

func (s *RedisPlanStore) DeletePlanForProject(owner, repo string, pullNum int, workspace, repoRelDir, projectName string) error {
	var planFilename string
	if projectName == "" {
		planFilename = workspace + ".tfplan"
	} else {
		planFilename = strings.ReplaceAll(projectName, "/", "::") + "-" + workspace + ".tfplan"
	}
	member := planMember(workspace, repoRelDir, planFilename)

	rctx, cancel := context.WithTimeout(context.Background(), redisOpTimeout)
	defer cancel()
	pipe := s.client.TxPipeline()
	pipe.Del(rctx, s.objKey(owner, repo, pullNum, member))
	pipe.SRem(rctx, s.indexKey(owner, repo, pullNum), member)
	if _, err := pipe.Exec(rctx); err != nil {
		s.logger.Warn("failed to delete plan from redis: %v", err)
	}
	return nil
}

var _ PlanStore = (*RedisPlanStore)(nil)
