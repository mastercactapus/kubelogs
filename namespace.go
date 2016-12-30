package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	log "github.com/sirupsen/logrus"
)

type namespace struct {
	o      *options
	name   string
	ctx    context.Context
	cancel context.CancelFunc
	pods   map[string]*pod
	l      *log.Entry
	es     *eventStream
}
type pod struct {
	o          *options
	ctx        context.Context
	cancel     context.CancelFunc
	containers map[string]*container
	l          *log.Entry
	namespace  string
	name       string
}
type container struct {
	o         *options
	ctx       context.Context
	cancel    context.CancelFunc
	ID        string
	namespace string
	pod       string
	name      string
	l         *log.Entry
}
type podStatus struct {
	ContainerStatuses []struct {
		Name        string
		ContainerID string
	}
}

func (c *cluster) newNamespace(name string) (*namespace, error) {
	ns := &namespace{
		name: name,
		o:    c.o,
		pods: make(map[string]*pod, 100),
		l:    c.l.WithField("namespace", name),
	}

	ns.ctx, ns.cancel = context.WithCancel(c.ctx)

	es, err := c.o.newEventStream(ns.ctx, "api/v1/namespaces/"+url.QueryEscape(name)+"/pods")
	if err != nil {
		ns.cancel()
		return nil, err
	}

	ns.es = es
	go ns.loop()

	return ns, nil
}

func (ct *container) log() {
	v := make(url.Values, 2)
	v.Set("follow", "true")
	v.Set("timestamps", "true")
	v.Set("container", ct.name)
	u := ct.o.buildEventURLWithQuery("api/v1/namespaces/"+url.QueryEscape(ct.namespace)+"/pods/"+url.QueryEscape(ct.pod)+"/log", v)

	req, err := http.NewRequest("GET", u, nil)
	if err != nil {
		ct.l.Errorln("failed to build request object:", err)
		return
	}
	req = req.WithContext(ct.ctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		ct.l.Errorln("failed to attach logs:", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		ct.l.Errorln("non-200 response for logs:", resp.Status)
		return
	}

	buf := bufio.NewReader(resp.Body)
	var line string
	var parts []string
	for {
		line, err = buf.ReadString('\n')
		if err == io.EOF {
			break
		}
		if err != nil {
			ct.l.Warnln(err)
			// reconnect?
			break
		}
		parts = strings.SplitN(line, " ", 2)
		ct.l.WithField("eventTime", parts[0]).Infoln(parts[1])
	}
}

func (p *pod) updateContainers(msg json.RawMessage) {
	var stat podStatus
	err := json.Unmarshal(msg, &stat)
	if err != nil {
		p.l.Errorln("failed to unmarshal pod status:", err, string(msg))
		return
	}
	var c *container
	for _, cs := range stat.ContainerStatuses {
		c = p.containers[cs.Name]
		if cs.ContainerID == "" {
			if c != nil {
				// container has ended
				c.cancel()
				delete(p.containers, cs.Name)
				c.l.Debugln("deleted container")
			}
			continue
		}

		if c != nil && c.ID == cs.ContainerID {
			// already up-to-date
			continue
		} else if c != nil {
			// new ID -- delete old one
			c.cancel()
			c.l.Debugln("deleted container")
		}

		c = &container{
			ID:        cs.ContainerID,
			l:         p.l.WithFields(log.Fields{"containerName": cs.Name, "containerID": cs.ContainerID}),
			namespace: p.namespace,
			pod:       p.name,
			name:      cs.Name,
			o:         p.o,
		}
		c.ctx, c.cancel = context.WithCancel(p.ctx)
		go c.log()
		c.l.Debugln("added container")
	}
}

func (ns *namespace) loop() {
	for e := range ns.es.C {
		if e.Object.Kind != "Pod" {
			ns.l.Warnln("unknown object type, expected 'Pod':", e.Object.Kind)
			continue
		}
		var p *pod
		switch e.Type {
		case eventTypeAdded:
			p = ns.pods[e.Object.Metadata.Name]
			if p != nil {
				// cleanup existing, if for some reason we got it again
				ns.l.Warnln("got ADDED event for already known pod:", e.Object.Metadata.Name)
				p.cancel()
				delete(ns.pods, e.Object.Metadata.Name)
			}
			p = &pod{
				containers: make(map[string]*container, 10),
				l:          ns.l.WithField("pod", e.Object.Metadata.Name),
				name:       e.Object.Metadata.Name,
				namespace:  ns.name,
				o:          ns.o,
			}
			p.ctx, p.cancel = context.WithCancel(ns.ctx)
			ns.pods[e.Object.Metadata.Name] = p
			p.updateContainers(e.Object.Status)
			p.l.Debugln("added pod")
		case eventTypeDeleted:
			p = ns.pods[e.Object.Metadata.Name]
			if p == nil {
				ns.l.Warnln("got DELETED event for unknown pod:", e.Object.Metadata.Name)
				continue
			}
			p.cancel()
			delete(ns.pods, e.Object.Metadata.Name)
			p.l.Debugln("deleted pod")
		case eventTypeModified:
			p = ns.pods[e.Object.Metadata.Name]
			if p != nil {
				// cleanup existing, if for some reason we got it again
				ns.l.Warnln("got MODIFIED event for unknown pod:", e.Object.Metadata.Name)
				continue
			}
			p.updateContainers(e.Object.Status)
			p.l.Debugln("modified pod")
		}

	}
}
