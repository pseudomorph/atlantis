// Code generated by pegomock. DO NOT EDIT.
package matchers

import (
	pegomock "github.com/petergtz/pegomock/v3"
	"reflect"

	models "github.com/runatlantis/atlantis/server/events/models"
)

func AnyModelsRepo() models.Repo {
	pegomock.RegisterMatcher(pegomock.NewAnyMatcher(reflect.TypeOf((*(models.Repo))(nil)).Elem()))
	var nullValue models.Repo
	return nullValue
}

func EqModelsRepo(value models.Repo) models.Repo {
	pegomock.RegisterMatcher(&pegomock.EqMatcher{Value: value})
	var nullValue models.Repo
	return nullValue
}

func NotEqModelsRepo(value models.Repo) models.Repo {
	pegomock.RegisterMatcher(&pegomock.NotEqMatcher{Value: value})
	var nullValue models.Repo
	return nullValue
}

func ModelsRepoThat(matcher pegomock.ArgumentMatcher) models.Repo {
	pegomock.RegisterMatcher(matcher)
	var nullValue models.Repo
	return nullValue
}
