package database

import (
	"reflect"
	"testing"
)

func TestPlanS3RoutingPins(t *testing.T) {
	unpinned := []string{"photos", "env-a", "logs", "env-b"}
	env := []string{"env-a", "env-b", "not-registered"}

	// No default config: nil already meant .env, nothing changes.
	if plan := planS3RoutingPins(unpinned, false, env); len(plan.Pin) != 0 || len(plan.EnvKept) != 0 {
		t.Errorf("without a default nothing may be pinned: %+v", plan)
	}

	// With a default: every unpinned bucket keeps the routing it had (the
	// default), except S3_BUCKETS buckets, which belong to the .env backend.
	plan := planS3RoutingPins(unpinned, true, env)
	if want := []string{"logs", "photos"}; !reflect.DeepEqual(plan.Pin, want) {
		t.Errorf("Pin = %v, want %v", plan.Pin, want)
	}
	if want := []string{"env-a", "env-b"}; !reflect.DeepEqual(plan.EnvKept, want) {
		t.Errorf("EnvKept = %v, want %v", plan.EnvKept, want)
	}

	if plan := planS3RoutingPins(nil, true, env); len(plan.Pin) != 0 || len(plan.EnvKept) != 0 {
		t.Errorf("no unpinned buckets: %+v", plan)
	}
}
