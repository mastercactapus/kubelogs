# kubelogs

Log aggregator for kubernetes.

`kubelogs` is a simple tool to aggregate logs from all namespaces, pods, and containers.

## Usage

kubelogs should work out-of-the-box with `kubectl proxy` running.

Cluster logs are written to `stdout` while errors/warnings/etc are written to `stderr`.
This is so that kubelogs can be used to pipe into something like logstash or netcat, even within the cluster.

## Events

Each line is treated as an event. Each event can have the following properties:

|Name|Description|
|---|---|
|containerID|The unique ID of the individual container|
|containerName|The name of the container in a pod|
|event|Decoded JSON data from the event (see below)|
|eventTime|The timestamp reported by kubernetes for the event|
|labels|The labels applied to the pod, if `-labels` is set (the default)|
|level|Always `info` for log events|
|msg|The message data from the event|
|namespace|The name of the namespace for the event|
|nodeName|The name of the node the event originated from|
|pod|The name of the pod|
|time|The local timestamp that the event was logged from `kubelogs`|

## JSON Events

If your app supports JSON logging directly, set the annotation `kubelogs/logformat` to `json`. 
You can also modify the message property with the annotation `kubelogs/messagefield`, the default is `msg`.

## Example Output

This config will launch a pod that outputs a json object every 5 seconds.

```yaml
kind: Pod
apiVersion: v1
metadata:
  name: pd-example
  labels:
    app: l-example
  annotations:
    kubelogs/logformat: json
spec:
  containers:
  - name: c-example
    image: alpine
    command: [sh]
    args:
    - -c
    - while true; do echo '{"msg":"hi","foo":"bar"}'; sleep 5; done
```

This is what you can expect to see with `kubelogs -json`:

```json
{"containerID":"docker://a9bb70ce49d54726dee25b5c7ac27c3969fb25601f4f2a397e268676a6ef4c31","containerName":"c-example","event":{"foo":"bar"},"eventTime":"2016-12-30T21:14:30.262487825Z","labels":{"app":"l-example"},"level":"info","msg":"hi","namespace":"default","nodeName":"minikube","pod":"pd-example","time":"2016-12-30T15:14:30-06:00"}
```

