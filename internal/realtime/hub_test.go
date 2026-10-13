package realtime

import (
	"testing"

	"github.com/FlameInTheDark/servy/internal/authz"
	"github.com/FlameInTheDark/servy/internal/events"
)

func TestCreationTopicStreamsOnlyToOwningUser(t *testing.T) {
	hub := &Hub{}
	owner := &client{userID: 17, principal: &authz.Principal{}, topics: map[string]bool{"creation": true}}
	other := &client{userID: 18, principal: &authz.Principal{}, topics: map[string]bool{"creation": true}}
	event := events.Event{
		Type:     events.EvSteamCmdOutput,
		ServerID: "not-yet-visible-to-client",
		Data:     map[string]interface{}{"line": "downloading", "user_id": int64(17)},
	}

	if !hub.canSubscribe(owner, "creation") {
		t.Fatal("authenticated clients must be able to subscribe to the creation progress topic")
	}
	if !hub.deliverTo(owner, event) {
		t.Fatal("SteamCMD progress was not delivered to the creating user")
	}
	if hub.deliverTo(other, event) {
		t.Fatal("SteamCMD progress leaked to a different user")
	}
	if got := topicFor(event); got != "creation" {
		t.Fatalf("SteamCMD progress topic = %q, want creation", got)
	}
}

func TestCreationTopicRequiresUserIdentity(t *testing.T) {
	hub := &Hub{}
	cl := &client{userID: 17, topics: map[string]bool{"creation": true}}
	event := events.Event{Type: events.EvSteamCmdOutput, Data: map[string]interface{}{"line": "private"}}
	if hub.deliverTo(cl, event) {
		t.Fatal("SteamCMD output without an owner must not be broadcast")
	}
}
