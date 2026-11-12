// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"html"
	"net/http"
	"sync"

	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
)

// WebhookDeliveries remembers the webhook deliveries already accepted, so
// servers sharing durable storage handle each delivery once.
type WebhookDeliveries interface {
	// RecordDelivery records a delivery and reports whether it is new.
	RecordDelivery(provider, id string) (bool, error)
	// ForgetDelivery removes a delivery that was not handled.
	ForgetDelivery(provider, id string) error
}

// deliveryHeaders names, in order of preference, the headers that carry each
// provider's delivery ID. Azure DevOps sends no delivery ID header.
var deliveryHeaders = map[models.VCSHostType][]string{
	models.Github:          {"X-GitHub-Delivery"},
	models.Gitlab:          {"X-Gitlab-Event-UUID"},
	models.BitbucketCloud:  {bitbucketCloudRequestIDHeader},
	models.BitbucketServer: {bitbucketServerRequestIDHeader},
	models.Gitea:           {giteaRequestIDHeader},
}

// acceptDelivery runs after a request is authenticated and before it has side
// effects. It records the delivery ID and returns false after answering a
// duplicate with 200. Requests without a delivery ID, and all requests without
// durable storage, are handled as before. The returned writer forgets the
// delivery when the response is not a success, so the provider's retry is
// handled.
func (e *VCSEventsController) acceptDelivery(w http.ResponseWriter, r *http.Request, host models.VCSHostType) (http.ResponseWriter, bool) {
	if e.Deliveries == nil {
		return w, true
	}
	var id string
	for _, header := range deliveryHeaders[host] {
		if id = r.Header.Get(header); id != "" {
			break
		}
	}
	if id == "" {
		return w, true
	}
	provider := host.String()
	first, err := e.Deliveries.RecordDelivery(provider, id)
	if err != nil {
		e.respond(w, logging.Error, http.StatusServiceUnavailable, "recording webhook delivery %s: %s", html.EscapeString(id), err)
		return w, false
	}
	if !first {
		e.respond(w, logging.Info, http.StatusOK, "Ignoring duplicate webhook delivery %s", html.EscapeString(id))
		return w, false
	}
	return &deliveryResponse{ResponseWriter: w, forget: func() {
		if err := e.Deliveries.ForgetDelivery(provider, id); err != nil {
			e.Logger.Err("forgetting unhandled webhook delivery %q %v", id, err)
		}
	}}, true
}

// deliveryResponse forgets its delivery when the handler answers with a status
// other than 2xx.
type deliveryResponse struct {
	http.ResponseWriter
	forget func()
	once   sync.Once
}

func (d *deliveryResponse) WriteHeader(code int) {
	if code < 200 || code > 299 {
		d.once.Do(d.forget)
	}
	d.ResponseWriter.WriteHeader(code)
}
