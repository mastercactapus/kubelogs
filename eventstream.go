package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	log "github.com/sirupsen/logrus"
)

type eventType string

const (
	eventTypeAdded    eventType = "ADDED"
	eventTypeModified eventType = "MODIFIED"
	eventTypeDeleted  eventType = "DELETED"
)

type event struct {
	Type   eventType
	Object struct {
		Kind       string
		APIVersion string
		Metadata   struct {
			Name        string
			SelfLink    string
			Labels      map[string]string
			Annotations map[string]string
		}
		Spec struct {
			NodeName string
		}
		Status json.RawMessage
	}
}
type eventStream struct {
	rc     io.ReadCloser
	C      chan *event
	cancel context.CancelFunc
}

func (e *eventStream) Close() error {
	e.cancel()
	return nil
}

func (e *eventStream) loop(log *log.Entry) {
	defer close(e.C)
	defer e.cancel()
	defer e.rc.Close()
	dec := json.NewDecoder(e.rc)
	var err error
	for {
		var evt event
		err = dec.Decode(&evt)
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Warnln("decode event failed:", err)
			break
		}
		e.C <- &event{Type: evt.Type, Object: evt.Object}
	}
}

func newEventStream(ctx context.Context, url string) (*eventStream, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	nCtx, cancel := context.WithCancel(ctx)
	req = req.WithContext(nCtx)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}

	if resp.StatusCode != 200 {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("non-200 response: %s", resp.Status)
	}

	e := &eventStream{
		cancel: cancel,
		rc:     resp.Body,
		C:      make(chan *event),
	}
	go e.loop(log.WithField("URL", url))
	return e, nil
}
