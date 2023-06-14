// Code generated by pegomock. DO NOT EDIT.
package matchers

import (
	pegomock "github.com/petergtz/pegomock/v3"
	"reflect"

	azuredevops "github.com/mcdafydd/go-azuredevops/azuredevops"
)

func AnyPtrToAzuredevopsGitPullRequest() *azuredevops.GitPullRequest {
	pegomock.RegisterMatcher(pegomock.NewAnyMatcher(reflect.TypeOf((*(*azuredevops.GitPullRequest))(nil)).Elem()))
	var nullValue *azuredevops.GitPullRequest
	return nullValue
}

func EqPtrToAzuredevopsGitPullRequest(value *azuredevops.GitPullRequest) *azuredevops.GitPullRequest {
	pegomock.RegisterMatcher(&pegomock.EqMatcher{Value: value})
	var nullValue *azuredevops.GitPullRequest
	return nullValue
}

func NotEqPtrToAzuredevopsGitPullRequest(value *azuredevops.GitPullRequest) *azuredevops.GitPullRequest {
	pegomock.RegisterMatcher(&pegomock.NotEqMatcher{Value: value})
	var nullValue *azuredevops.GitPullRequest
	return nullValue
}

func PtrToAzuredevopsGitPullRequestThat(matcher pegomock.ArgumentMatcher) *azuredevops.GitPullRequest {
	pegomock.RegisterMatcher(matcher)
	var nullValue *azuredevops.GitPullRequest
	return nullValue
}
