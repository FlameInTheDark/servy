package events

import (
        "sync"
        "testing"
        "time"
)

// TestBusDeliverSubscribeReceive covers basic fan-out semantics: a "*"
// subscriber receives both panel-wide and server-scoped events, and a
// topic subscriber only receives its own topic.
func TestBusDeliverSubscribeReceive(t *testing.T) {
        b := NewBus()
        all, cancelAll := b.Subscribe("*")
        srv, cancelSrv := b.Subscribe("server.s1")
        defer cancelAll()
        defer cancelSrv()

        b.PublishSimple("host_stats", 1)
        b.Publish(Event{Type: "server_start", ServerID: "s1"})
        b.Publish(Event{Type: "server_start", ServerID: "s2"})

        deadline := time.After(2 * time.Second)
        var got []Event
        for len(got) < 3 {
                select {
                case ev := <-all:
                        got = append(got, ev)
                case <-deadline:
                        t.Fatalf("subscriber received %d events, want 3", len(got))
                }
        }
        select {
        case ev := <-srv:
                if ev.ServerID != "s1" {
                        t.Errorf("server topic leaked foreign event: %+v", ev)
                }
        case <-time.After(200 * time.Millisecond):
                t.Fatal("server topic subscriber got nothing")
        }
}

// TestBusPublishCancelRace reproduces the send-on-closed-channel panic:
// concurrent publishers vs rapid subscribe/cancel churn. Run with -race
// (CI does); before the fix this test dead-panicked or tripped the
// detector on chansend vs closechan.
func TestBusPublishCancelRace(t *testing.T) {
        b := NewBus()
        stop := make(chan struct{})
        var wg sync.WaitGroup

        // concurrent publishers, panel-wide + server-scoped
        for i := 0; i < 4; i++ {
                wg.Add(1)
                go func(i int) {
                        defer wg.Done()
                        for {
                                select {
                                case <-stop:
                                        return
                                default:
                                }
                                if i%2 == 0 {
                                        b.PublishSimple("host_stats", i)
                                } else {
                                        b.Publish(Event{Type: "server_stats", ServerID: "s1"})
                                }
                        }
                }(i)
        }

        // subscriber churn: subscribe, drain a little, cancel (closes channel)
        for i := 0; i < 500; i++ {
                ch, cancel := b.Subscribe("*")
                b.PublishSimple("tick", i)
                select {
                case <-ch:
                default:
                }
                cancel()
        }

        close(stop)
        wg.Wait()
}

// TestBusCancelIdempotent ensures double-cancel never double-closes.
func TestBusCancelIdempotent(t *testing.T) {
        b := NewBus()
        _, cancel := b.Subscribe("*")
        cancel()
        cancel() // must not panic
        b.PublishSimple("after", 1)
}
