package projectlock

import "testing"

func TestCutoverExcludesReconcileOnlyForSameProject(t *testing.T) {
	unlock := Lock(100)
	if release, ok := TryLock(100); ok {
		release()
		t.Fatal("reconcile entered a project owned by cutover")
	}
	release, ok := TryLock(101)
	if !ok {
		t.Fatal("cutover blocked another project")
	}
	release()
	unlock()
	release, ok = TryLock(100)
	if !ok {
		t.Fatal("project remained locked after cutover")
	}
	release()
}
