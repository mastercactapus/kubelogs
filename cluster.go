package main

import "context"
import log "github.com/sirupsen/logrus"

type cluster struct {
	ctx        context.Context
	namespaces map[string]*namespace
	o          *options
	es         *eventStream
	l          *log.Entry
}

func newCluster(o *options) (*cluster, error) {
	root := context.Background()
	es, err := o.newEventStream(root, "api/v1/namespaces")
	if err != nil {
		return nil, err
	}
	c := &cluster{
		ctx:        root,
		es:         es,
		namespaces: make(map[string]*namespace, 20),
		l:          log.WithField("cluster", o.u.String()),
		o:          o,
	}
	return c, nil
}

func (c *cluster) loop() {
	for e := range c.es.C {
		if e.Object.Kind != "Namespace" {
			c.l.Warnln("unknown object type, expected 'Namespace':", e.Object.Kind)
			continue
		}
		switch e.Type {
		case eventTypeAdded:
			if ns := c.namespaces[e.Object.Metadata.Name]; ns != nil {
				// cleanup existing, if for some reason we got it again
				c.l.Warnln("got ADDED event for already known namespace:", e.Object.Metadata.Name)
				ns.cancel()
				delete(c.namespaces, e.Object.Metadata.Name)
			}
			ns, err := c.newNamespace(e.Object.Metadata.Name)
			if err != nil {
				c.l.Warnf("failed to add namespace '%s': %s", e.Object.Metadata.Name, err.Error())
				continue
			}
			c.namespaces[e.Object.Metadata.Name] = ns
			ns.l.Debugln("added namespace")
		case eventTypeDeleted:
			ns := c.namespaces[e.Object.Metadata.Name]
			if ns == nil {
				c.l.Warnln("got DELETED event for unknown namespace:", e.Object.Metadata.Name)
				continue
			}
			ns.cancel()
			delete(c.namespaces, e.Object.Metadata.Name)
			ns.l.Debugln("deleted namespace")
		}
	}
}
