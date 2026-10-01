package awsconfig

import "testing"

func TestDisplayAccessors(t *testing.T) {
	sso := Profile{Keys: map[string]string{
		"sso_account_id": "111111111111",
		"sso_role_name":  "ReadOnly",
		"region":         "us-east-1",
		"sso_region":     "ap-southeast-2",
	}}
	if sso.AccountID() != "111111111111" || sso.RoleName() != "ReadOnly" {
		t.Fatalf("sso accessors: %q %q", sso.AccountID(), sso.RoleName())
	}
	// Region is the plain key, never sso_region (that is where Identity
	// Center lives, not where workloads run).
	if sso.Region() != "us-east-1" {
		t.Fatalf("Region must be the plain key, got %q", sso.Region())
	}

	granted := Profile{Keys: map[string]string{
		"granted_sso_account_id": "333333333333",
		"granted_sso_role_name":  "PowerUser",
	}}
	if granted.AccountID() != "333333333333" || granted.RoleName() != "PowerUser" {
		t.Fatalf("granted_ variants: %q %q", granted.AccountID(), granted.RoleName())
	}

	arn := Profile{Keys: map[string]string{
		"role_arn":   "arn:aws:iam::222222222222:role/AdminRole",
		"mfa_serial": "arn:aws:iam::111111111111:mfa/ken",
	}}
	if arn.AccountID() != "222222222222" || arn.RoleName() != "AdminRole" {
		t.Fatalf("role_arn derivation: %q %q", arn.AccountID(), arn.RoleName())
	}
	if !arn.NeedsMFA() {
		t.Fatal("mfa_serial must set NeedsMFA")
	}

	// Static-key profiles have neither, and that is fine.
	static := Profile{Keys: map[string]string{"aws_access_key_id": "AKIA"}}
	if static.AccountID() != "" || static.RoleName() != "" || static.NeedsMFA() {
		t.Fatalf("static profile should have no account/role/mfa: %+v", static)
	}
}
