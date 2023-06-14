// Code generated by pegomock. DO NOT EDIT.
// Source: github.com/runatlantis/atlantis/server/core/locking (interfaces: ApplyLockChecker)

package mocks

import (
	pegomock "github.com/petergtz/pegomock/v3"
	locking "github.com/runatlantis/atlantis/server/core/locking"
	"reflect"
	"time"
)

type MockApplyLockChecker struct {
	fail func(message string, callerSkip ...int)
}

func NewMockApplyLockChecker(options ...pegomock.Option) *MockApplyLockChecker {
	mock := &MockApplyLockChecker{}
	for _, option := range options {
		option.Apply(mock)
	}
	return mock
}

func (mock *MockApplyLockChecker) SetFailHandler(fh pegomock.FailHandler) { mock.fail = fh }
func (mock *MockApplyLockChecker) FailHandler() pegomock.FailHandler      { return mock.fail }

func (mock *MockApplyLockChecker) CheckApplyLock() (locking.ApplyCommandLock, error) {
	if mock == nil {
		panic("mock must not be nil. Use myMock := NewMockApplyLockChecker().")
	}
	params := []pegomock.Param{}
	result := pegomock.GetGenericMockFrom(mock).Invoke("CheckApplyLock", params, []reflect.Type{reflect.TypeOf((*locking.ApplyCommandLock)(nil)).Elem(), reflect.TypeOf((*error)(nil)).Elem()})
	var ret0 locking.ApplyCommandLock
	var ret1 error
	if len(result) != 0 {
		if result[0] != nil {
			ret0 = result[0].(locking.ApplyCommandLock)
		}
		if result[1] != nil {
			ret1 = result[1].(error)
		}
	}
	return ret0, ret1
}

func (mock *MockApplyLockChecker) VerifyWasCalledOnce() *VerifierMockApplyLockChecker {
	return &VerifierMockApplyLockChecker{
		mock:                   mock,
		invocationCountMatcher: pegomock.Times(1),
	}
}

func (mock *MockApplyLockChecker) VerifyWasCalled(invocationCountMatcher pegomock.InvocationCountMatcher) *VerifierMockApplyLockChecker {
	return &VerifierMockApplyLockChecker{
		mock:                   mock,
		invocationCountMatcher: invocationCountMatcher,
	}
}

func (mock *MockApplyLockChecker) VerifyWasCalledInOrder(invocationCountMatcher pegomock.InvocationCountMatcher, inOrderContext *pegomock.InOrderContext) *VerifierMockApplyLockChecker {
	return &VerifierMockApplyLockChecker{
		mock:                   mock,
		invocationCountMatcher: invocationCountMatcher,
		inOrderContext:         inOrderContext,
	}
}

func (mock *MockApplyLockChecker) VerifyWasCalledEventually(invocationCountMatcher pegomock.InvocationCountMatcher, timeout time.Duration) *VerifierMockApplyLockChecker {
	return &VerifierMockApplyLockChecker{
		mock:                   mock,
		invocationCountMatcher: invocationCountMatcher,
		timeout:                timeout,
	}
}

type VerifierMockApplyLockChecker struct {
	mock                   *MockApplyLockChecker
	invocationCountMatcher pegomock.InvocationCountMatcher
	inOrderContext         *pegomock.InOrderContext
	timeout                time.Duration
}

func (verifier *VerifierMockApplyLockChecker) CheckApplyLock() *MockApplyLockChecker_CheckApplyLock_OngoingVerification {
	params := []pegomock.Param{}
	methodInvocations := pegomock.GetGenericMockFrom(verifier.mock).Verify(verifier.inOrderContext, verifier.invocationCountMatcher, "CheckApplyLock", params, verifier.timeout)
	return &MockApplyLockChecker_CheckApplyLock_OngoingVerification{mock: verifier.mock, methodInvocations: methodInvocations}
}

type MockApplyLockChecker_CheckApplyLock_OngoingVerification struct {
	mock              *MockApplyLockChecker
	methodInvocations []pegomock.MethodInvocation
}

func (c *MockApplyLockChecker_CheckApplyLock_OngoingVerification) GetCapturedArguments() {
}

func (c *MockApplyLockChecker_CheckApplyLock_OngoingVerification) GetAllCapturedArguments() {
}
