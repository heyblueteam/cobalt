package cobaltfile

import (
	"reflect"
	"testing"
)

func TestParse_NotifyDefaults(t *testing.T) {
	cf, err := Parse([]byte(`{
		"version": "1.0",
		"notify": {"telegram": {}}
	}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tg := cf.Notify.Telegram
	if tg == nil {
		t.Fatal("telegram config is nil")
	}
	if !tg.IsEnabled() {
		t.Error("an omitted enabled should default to true")
	}
	if want := DefaultNotifyEvents(); !reflect.DeepEqual(tg.On, want) {
		t.Errorf("on: got %v, want %v", tg.On, want)
	}
}

func TestParse_NotifyExplicitValues(t *testing.T) {
	cf, err := Parse([]byte(`{
		"version": "1.0",
		"notify": {"telegram": {
			"enabled": false,
			"chat": "-1001",
			"on": ["failure", "canceled"],
			"templateFile": ".cobalt/telegram.tmpl"
		}}
	}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	tg := cf.Notify.Telegram
	if tg.IsEnabled() {
		t.Error("explicit false should stay false")
	}
	if tg.Chat != "-1001" {
		t.Errorf("chat: got %q", tg.Chat)
	}
	if want := []string{"failure", "canceled"}; !reflect.DeepEqual(tg.On, want) {
		t.Errorf("on: got %v, want %v", tg.On, want)
	}
	if tg.TemplateFile != ".cobalt/telegram.tmpl" {
		t.Errorf("templateFile: got %q", tg.TemplateFile)
	}
}

func TestParse_NotifyRejectsUnknownEvent(t *testing.T) {
	_, err := Parse([]byte(`{
		"version": "1.0",
		"notify": {"telegram": {"on": ["deployed"]}}
	}`))
	if err == nil {
		t.Fatal("expected an error for an unknown event")
	}
}

func TestParse_NotifyRejectsTemplateOutsideProject(t *testing.T) {
	for _, name := range []string{"../telegram.tmpl", "/etc/telegram.tmpl", "."} {
		_, err := Parse([]byte(`{
			"version": "1.0",
			"notify": {"telegram": {"templateFile": "` + name + `"}}
		}`))
		if err == nil {
			t.Errorf("%q: expected an error", name)
		}
	}
}

func TestParse_NoNotifyBlockDisablesNotifications(t *testing.T) {
	cf, err := Parse([]byte(`{"version": "1.0"}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cf.Notify != nil {
		t.Fatalf("notify: got %+v, want nil", cf.Notify)
	}
}
