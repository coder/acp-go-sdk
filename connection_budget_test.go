package acp

import (
	"testing"
)

func TestConnectionOption_PanicsBelowOne(t *testing.T) {
	options := map[string]func(int) ConnectionOption{
		"WithByteBudget":             WithByteBudget,
		"WithMaxInboundRequests":     WithMaxInboundRequests,
		"WithMaxQueuedNotifications": WithMaxQueuedNotifications,
	}
	for name, option := range options {
		for _, n := range []int{0, -1} {
			func() {
				defer func() {
					if recover() == nil {
						t.Errorf("%s(%d) did not panic", name, n)
					}
				}()
				option(n)
			}()
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s(1) panicked: %v", name, r)
				}
			}()
			option(1)
		}()
	}
}
