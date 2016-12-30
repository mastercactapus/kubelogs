package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	logFormatAnnotation       = "kubelogs/logformat"
	logMessageFieldAnnotation = "kubelogs/messagefield"
)

type cluster struct {
	o      *options
	name   string
	ctx    context.Context
	cancel context.CancelFunc
	pods   map[string]*pod
	l      *log.Entry
	es     *eventStream
}
type pod struct {
	o           *options
	ctx         context.Context
	cancel      context.CancelFunc
	containers  map[string]*container
	l           *log.Entry
	namespace   string
	name        string
	decodeField string
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

func newCluster(o *options) (*cluster, error) {
	c := &cluster{
		name: o.clusterName,
		o:    o,
		pods: make(map[string]*pod, 100),
		l:    log.WithField("cluster", o.clusterName),
	}

	c.ctx, c.cancel = context.WithCancel(context.Background())

	es, err := o.newEventStream(c.ctx, "api/v1/pods")
	if err != nil {
		c.cancel()
		return nil, err
	}

	c.es = es

	return c, nil
}

func (ct *container) getJSONFields(data, messageField string) (log.Fields, string, error) {
	f := make(log.Fields)
	err := json.Unmarshal([]byte(data), &f)
	if err != nil {
		return nil, "", err
	}
	msg, ok := f[messageField].(string)
	if !ok && f[messageField] != nil {
		ct.l.Warnln("message was not a string:", f[messageField])
	} else {
		delete(f, messageField)
	}

	return f, msg, nil
}

func (ct *container) log(decodeField string) {
	v := make(url.Values, 2)
	v.Set("follow", "true")
	v.Set("timestamps", "true")
	v.Set("container", ct.name)
	if ct.o.since >= 0 {
		v.Set("sinceSeconds", strconv.Itoa(int(ct.o.since.Seconds())))
	}
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
			ct.l.Warnln("read error:", err)
			resp.Body.Close()
			time.Sleep(3 * time.Second)
			ct.l.Debugln("reconnecting")
			go ct.log(decodeField)
			break
		}
		parts = strings.SplitN(line, " ", 2)
		if decodeField == "" {
			ct.o.out.WithFields(ct.l.WithField("eventTime", parts[0]).Data).Infoln(parts[1])
		} else {
			f, msg, err := ct.getJSONFields(parts[1], decodeField)
			if err != nil {
				ct.l.Warnln("failed to parse JSON:", err)
				ct.o.out.WithFields(ct.l.WithField("eventTime", parts[0]).Data).Infoln(parts[1])
				continue
			}
			ct.o.out.WithFields(ct.l.WithField("event", f).WithField("eventTime", parts[0]).Data).Infoln(msg)
		}
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
		go c.log(p.decodeField)
		c.l.Debugln("added container")
	}
}

func (c *cluster) loop() {
	for e := range c.es.C {
		if e.Object.Kind != "Pod" {
			c.l.Warnln("unknown object type, expected 'Pod':", e.Object.Kind)
			continue
		}
		var p *pod
		switch e.Type {
		case eventTypeAdded:
			p = c.pods[e.Object.Metadata.Name]
			if p != nil {
				// cleanup existing, if for some reason we got it again
				c.l.Warnln("got ADDED event for already known pod:", e.Object.Metadata.Name)
				p.cancel()
				delete(c.pods, e.Object.Metadata.Name)
			}
			p = &pod{
				containers: make(map[string]*container, 10),
				l:          c.l.WithField("pod", e.Object.Metadata.Name).WithField("nodeName", e.Object.Spec.NodeName).WithField("namespace", e.Object.Metadata.Namespace),
				name:       e.Object.Metadata.Name,
				namespace:  e.Object.Metadata.Namespace,
				o:          c.o,
			}
			if c.o.mergeLabels && e.Object.Metadata.Labels != nil {
				p.l = p.l.WithField("labels", e.Object.Metadata.Labels)
			}

			if c.o.decode && e.Object.Metadata.Annotations != nil && e.Object.Metadata.Annotations[logFormatAnnotation] == "json" {
				decodeField := e.Object.Metadata.Annotations[logMessageFieldAnnotation]
				if decodeField == "" {
					decodeField = "msg"
				}

				p.decodeField = decodeField
			}
			p.ctx, p.cancel = context.WithCancel(c.ctx)
			c.pods[e.Object.Metadata.Name] = p
			p.updateContainers(e.Object.Status)
			p.l.Debugln("added pod")
		case eventTypeDeleted:
			p = c.pods[e.Object.Metadata.Name]
			if p == nil {
				c.l.Warnln("got DELETED event for unknown pod:", e.Object.Metadata.Name)
				continue
			}
			p.cancel()
			delete(c.pods, e.Object.Metadata.Name)
			p.l.Debugln("deleted pod")
		case eventTypeModified:
			p = c.pods[e.Object.Metadata.Name]
			if p == nil {
				c.l.Warnln("got MODIFIED event for unknown pod:", e.Object.Metadata.Name)
				continue
			}
			p.updateContainers(e.Object.Status)
			p.l.Debugln("modified pod")
		}

	}
}
