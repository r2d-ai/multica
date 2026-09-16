package r2dauth

import "testing"

// TestProjectRolesForOperation pins the explicit-role ladder used by the P07-C
// notification fan-out. The set is derived from Decision.Can, so this test also
// guards against the two drifting apart.
func TestProjectRolesForOperation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		op   Operation
		want []ProjectRole
	}{
		{OperationRead, []ProjectRole{ProjectRoleViewer, ProjectRoleMember, ProjectRoleManager}},
		{OperationContribute, []ProjectRole{ProjectRoleMember, ProjectRoleManager}},
		{OperationManage, []ProjectRole{ProjectRoleManager}},
		{OperationShare, []ProjectRole{ProjectRoleManager}},
		{Operation("bogus"), nil},
	}

	for _, tc := range cases {
		got := ProjectRolesForOperation(tc.op)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: roles = %v, want %v", tc.op, got, tc.want)
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%s: roles[%d] = %q, want %q", tc.op, i, got[i], tc.want[i])
			}
		}
	}

	// Every role/operation pair must agree with the policy's own answer.
	for _, role := range []ProjectRole{ProjectRoleViewer, ProjectRoleMember, ProjectRoleManager} {
		for _, op := range []Operation{OperationRead, OperationContribute, OperationManage, OperationShare} {
			inSet := false
			for _, r := range ProjectRolesForOperation(op) {
				if r == role {
					inSet = true
					break
				}
			}
			if want := (Decision{Role: role, valid: true}).Can(op); inSet != want {
				t.Fatalf("role %q / op %q: in set = %v, policy allows = %v", role, op, inSet, want)
			}
		}
	}
}
