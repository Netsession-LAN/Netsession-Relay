package otlp

import (
	"fmt"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"github.com/Netsession-LAN/Netsession-Relay/internal/notify"
)

// Encoding into OTLP protobuf messages: attribute values, the resource, the metrics and the log records of events.

func anyValue(v any) *commonpb.AnyValue {
	switch x := v.(type) {
	case string:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: x}}
	case int64:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: x}}
	case int:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: int64(x)}}
	case bool:
		return &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: x}}
	}
	return &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: fmt.Sprint(v)}}
}

func attributes(kvs []KV) []*commonpb.KeyValue {
	out := make([]*commonpb.KeyValue, 0, len(kvs))
	for _, kv := range kvs {
		out = append(out, &commonpb.KeyValue{Key: kv.Key, Value: anyValue(kv.Value)})
	}
	return out
}

func resource(r Resource) *resourcepb.Resource {
	kvs := []KV{{"service.name", r.ServiceName}}
	for _, kv := range []KV{{"service.version", r.Version}, {"service.instance.id", r.InstanceID}, {"host.name", r.HostName}} {
		if kv.Value != "" {
			kvs = append(kvs, kv)
		}
	}
	return &resourcepb.Resource{Attributes: attributes(kvs)}
}

func nanos(t time.Time) uint64 {
	if t.IsZero() {
		return 0
	}
	return uint64(t.UnixNano())
}

func resourceMetrics(r Resource, ms []Metric, _ bool) []*metricspb.ResourceMetrics {
	out := make([]*metricspb.Metric, 0, len(ms))
	for _, m := range ms {
		pts := make([]*metricspb.NumberDataPoint, 0, len(m.Points))
		for _, p := range m.Points {
			dp := &metricspb.NumberDataPoint{Attributes: attributes(p.Attrs), TimeUnixNano: nanos(m.Time),
				Value: &metricspb.NumberDataPoint_AsInt{AsInt: p.Value}}
			if m.Sum {
				dp.StartTimeUnixNano = nanos(m.Start)
			}
			pts = append(pts, dp)
		}
		pm := &metricspb.Metric{Name: m.Name, Unit: m.Unit, Description: m.Desc}
		if m.Sum {
			pm.Data = &metricspb.Metric_Sum{Sum: &metricspb.Sum{DataPoints: pts, IsMonotonic: true,
				AggregationTemporality: metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE}}
		} else {
			pm.Data = &metricspb.Metric_Gauge{Gauge: &metricspb.Gauge{DataPoints: pts}}
		}
		out = append(out, pm)
	}
	return []*metricspb.ResourceMetrics{{Resource: resource(r), ScopeMetrics: []*metricspb.ScopeMetrics{{
		Scope: &commonpb.InstrumentationScope{Name: "nsrelay", Version: r.Version}, Metrics: out}}}}
}

// severity maps an event to the OTLP severity: WARN only for opened alarms (filter by severity in Zabbix),
// INFO2 for audit entries of dangerous actions, INFO for everything else.
func severity(e notify.Event) (logspb.SeverityNumber, string) {
	switch {
	case e.Type == notify.TypeAlarm && e.State == "open":
		return logspb.SeverityNumber_SEVERITY_NUMBER_WARN, "WARN"
	case e.Type == notify.TypeAudit && e.Severity == notify.Warn:
		return logspb.SeverityNumber_SEVERITY_NUMBER_INFO2, "INFO2"
	}
	return logspb.SeverityNumber_SEVERITY_NUMBER_INFO, "INFO"
}

// logAttributes are the attributes of the log record of e; empty values are left out.
func logAttributes(e notify.Event) []KV {
	kvs := []KV{{"nsrelay.msgid", e.MsgID()}}
	add := func(k, v string) {
		if v != "" {
			kvs = append(kvs, KV{k, v})
		}
	}
	if e.Type == notify.TypeAlarm {
		add("nsrelay.alarm.kind", e.Kind)
		add("nsrelay.alarm.key", e.Key)
		add("nsrelay.alarm.state", e.State)
	}
	if e.VLAN > 0 {
		kvs = append(kvs, KV{"vlan", int64(e.VLAN)})
	}
	if e.Type == notify.TypeAudit {
		add("nsrelay.audit.action", e.Action)
		add("nsrelay.audit.object", e.Object)
	}
	add("user.name", e.User)
	return kvs
}

func resourceLogs(r Resource, batch []notify.Event) []*logspb.ResourceLogs {
	now := time.Now()
	recs := make([]*logspb.LogRecord, 0, len(batch))
	for _, e := range batch {
		num, text := severity(e)
		recs = append(recs, &logspb.LogRecord{TimeUnixNano: nanos(e.Time), ObservedTimeUnixNano: nanos(now),
			SeverityNumber: num, SeverityText: text, Body: anyValue(e.Text), Attributes: attributes(logAttributes(e))})
	}
	return []*logspb.ResourceLogs{{Resource: resource(r), ScopeLogs: []*logspb.ScopeLogs{{
		Scope: &commonpb.InstrumentationScope{Name: "nsrelay", Version: r.Version}, LogRecords: recs}}}}
}
