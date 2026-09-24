// Package sshlink watches one SSH client connection for the two in-process
// SSH clients FileES has (the activation tunnel, pkg/deploy, and the svn+ssh
// tunnel, pkg/sshexec): it closes the connection when the context ends or
// the peer stops answering, and remembers which of the two it was, so the
// error the closing produces can be replaced by the reason.
package sshlink

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// Link is the watch over one connection.
type Link struct {
	client *ssh.Client
	stop   chan struct{}
	once   sync.Once

	mu    sync.Mutex
	cause error
}

// Watch starts watching client. interval and max are OpenSSH's
// ServerAliveInterval and ServerAliveCountMax: a probe every interval, and
// the connection is given up after max probes in a row went unanswered. Any
// reply counts, including a refusal of the request. host names the server in
// OpenSSH's own sentence, which pkg/errmap reads as a dropped connection.
func Watch(ctx context.Context, client *ssh.Client, interval time.Duration, max int, host string) *Link {
	link := &Link{client: client, stop: make(chan struct{})}
	go func() {
		select {
		case <-ctx.Done():
			link.closeWith(ctx.Err())
		case <-link.stop:
		}
	}()
	if interval > 0 && max > 0 {
		go link.keepAlive(interval, max, host)
	}
	return link
}

// Stop ends the watch; it does not close the connection.
func (link *Link) Stop() {
	link.once.Do(func() { close(link.stop) })
}

// Cause returns why the watch closed the connection, or err when it did not.
func (link *Link) Cause(err error) error {
	link.mu.Lock()
	defer link.mu.Unlock()
	if link.cause != nil {
		return link.cause
	}
	return err
}

func (link *Link) closeWith(err error) {
	link.mu.Lock()
	first := link.cause == nil
	if first {
		link.cause = err
	}
	link.mu.Unlock()
	if first {
		_ = link.client.Close()
	}
}

func (link *Link) keepAlive(interval time.Duration, max int, host string) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	missed := 0
	for {
		select {
		case <-link.stop:
			return
		case <-ticker.C:
		}
		replied := make(chan struct{})
		go func() {
			if _, _, err := link.client.SendRequest("keepalive@openssh.com", true, nil); err == nil {
				close(replied)
			}
		}()
		select {
		case <-link.stop:
			return
		case <-replied:
			missed = 0
		case <-time.After(interval):
			missed++
			if missed >= max {
				link.closeWith(fmt.Errorf("Timeout, server %s not responding.", host))
				return
			}
		}
	}
}
