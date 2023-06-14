// Code generated by pegomock. DO NOT EDIT.
package matchers

import (
	pegomock "github.com/petergtz/pegomock/v3"
	"reflect"

	valid "github.com/runatlantis/atlantis/server/core/config/valid"
)

func AnyValidPolicySet() valid.PolicySet {
	pegomock.RegisterMatcher(pegomock.NewAnyMatcher(reflect.TypeOf((*(valid.PolicySet))(nil)).Elem()))
	var nullValue valid.PolicySet
	return nullValue
}

func EqValidPolicySet(value valid.PolicySet) valid.PolicySet {
	pegomock.RegisterMatcher(&pegomock.EqMatcher{Value: value})
	var nullValue valid.PolicySet
	return nullValue
}

func NotEqValidPolicySet(value valid.PolicySet) valid.PolicySet {
	pegomock.RegisterMatcher(&pegomock.NotEqMatcher{Value: value})
	var nullValue valid.PolicySet
	return nullValue
}

func ValidPolicySetThat(matcher pegomock.ArgumentMatcher) valid.PolicySet {
	pegomock.RegisterMatcher(matcher)
	var nullValue valid.PolicySet
	return nullValue
}
