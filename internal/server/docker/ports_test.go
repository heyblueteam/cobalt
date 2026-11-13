package docker

import (
	"context"
	"reflect"
	"testing"
)

func TestServicePublishedPorts(t *testing.T) {
	for _, tc := range []struct {
		name, out string
		want      []PublishedPort
		fail      bool
	}{
		{"none", "null", nil, false},
		{"empty", "", nil, false},
		{
			"ingress and host", `[{"Protocol":"tcp","TargetPort":587,"PublishedPort":587,"PublishMode":"ingress"},{"Protocol":"udp","TargetPort":53,"PublishedPort":5353,"PublishMode":"host"}]`,
			[]PublishedPort{{PublishedAs: 587, FromContainerPort: 587, Protocol: "tcp", Mode: "ingress"}, {PublishedAs: 5353, FromContainerPort: 53, Protocol: "udp", Mode: "host"}},
			false,
		},
		{"target only", `[{"Protocol":"tcp","TargetPort":80,"PublishedPort":0,"PublishMode":"ingress"}]`, []PublishedPort{}, false},
		{"garbage", "<no value>", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newFakeRunner()
			r.answerStdout("service inspect", tc.out)
			got, err := NewWithRunner(r).ServicePublishedPorts(context.Background(), "demo-1-web")
			if (err != nil) != tc.fail {
				t.Fatalf("err %v", err)
			}
			if !tc.fail && !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v", got)
			}
		})
	}
}

// --publish-rm needs the long form: the short `--publish-rm 587` matches the
// target port and silently removes nothing when published != target.
func TestReleaseAndRestoreServicePortsUseLongForm(t *testing.T) {
	ports := []PublishedPort{
		{PublishedAs: 587, FromContainerPort: 587, Protocol: "tcp", Mode: "ingress"},
		{PublishedAs: 4465, FromContainerPort: 465},
	}
	for flag, call := range map[string]func(*Client) error{
		"--publish-rm":  func(c *Client) error { return c.ReleaseServicePorts(context.Background(), "demo-4-web", ports) },
		"--publish-add": func(c *Client) error { return c.RestoreServicePorts(context.Background(), "demo-4-web", ports) },
	} {
		r := newFakeRunner()
		if err := call(NewWithRunner(r)); err != nil {
			t.Fatal(err)
		}
		want := []string{
			"service", "update", "--detach=true",
			flag, "published=587,target=587,protocol=tcp,mode=ingress",
			flag, "published=4465,target=465,protocol=tcp",
			"demo-4-web",
		}
		if got := r.lastCall().Args; !reflect.DeepEqual(got, want) {
			t.Fatalf("%s args %v", flag, got)
		}
	}
}

func TestServicePortsNoopWithoutPorts(t *testing.T) {
	r := newFakeRunner()
	c := NewWithRunner(r)
	if err := c.ReleaseServicePorts(context.Background(), "demo", nil); err != nil {
		t.Fatal(err)
	}
	if err := c.RestoreServicePorts(context.Background(), "demo", nil); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 0 {
		t.Fatal(r.calls)
	}
}
