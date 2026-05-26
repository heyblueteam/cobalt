package docker

import (
	"context"
	"testing"
)

func TestListInternalImages_Parses(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout(
		"image ls",
		"cobalt/project-api-default:3\tsha256-aaa\n"+
			"cobalt/project-api-default:4\tsha256-bbb\n"+
			"cobalt/project-api-worker:3\tsha256-ccc\n",
	)
	c := NewWithRunner(r)
	got, err := c.ListInternalImages(context.Background(), "api")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d, want 3", len(got))
	}
	if got[0].Tag != "cobalt/project-api-default:3" || got[0].DeploymentNumber != 3 {
		t.Errorf("[0]: %+v", got[0])
	}
	if got[2].Repository != "cobalt/project-api-worker" {
		t.Errorf("[2] repo: %q", got[2].Repository)
	}
}

func TestListInternalImages_SkipsMalformed(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerStdout(
		"image ls",
		"cobalt/project-api-default:latest\tid1\n"+ // non-numeric tag → skip
			"cobalt/project-api-default\tid2\n"+ // no tag → skip
			"cobalt/project-api-default:5\tid3\n",
	)
	c := NewWithRunner(r)
	got, _ := c.ListInternalImages(context.Background(), "api")
	if len(got) != 1 {
		t.Errorf("got %d images, want 1", len(got))
	}
	if got[0].DeploymentNumber != 5 {
		t.Errorf("[0]: %+v", got[0])
	}
}

func TestRemoveImage_TreatsMissingAsSuccess(t *testing.T) {
	t.Parallel()
	r := newFakeRunner()
	r.answerErr("image rm", staticErr("Error response from daemon: No such image: foo"))
	c := NewWithRunner(r)
	if err := c.RemoveImage(context.Background(), "foo"); err != nil {
		t.Errorf("RemoveImage: %v", err)
	}
}
