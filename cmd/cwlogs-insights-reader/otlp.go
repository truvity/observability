package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	collogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"
)

// OTLPSink posts events as OTLP/HTTP protobuf to the collector in the same pod.
//
// The records look like the `awscloudwatch` receiver's: resource attributes
// aws.region, cloudwatch.log.group.name and cloudwatch.log.stream, the event
// text as the body, the event time as the record timestamp. The collector's
// filter and transform processors are therefore the same in both read modes.
type OTLPSink struct {
	URL      string
	Region   string
	LogGroup string
	HTTP     *http.Client
	Retries  int
	Sleep    func(ctx context.Context, d time.Duration) error
}

func str(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

// Request builds the OTLP request for events (one ResourceLogs per stream).
func (s *OTLPSink) Request(events []Event, observed time.Time) *collogs.ExportLogsServiceRequest {
	var order []string

	byStream := map[string][]*logspb.LogRecord{}

	for _, e := range events {
		if _, ok := byStream[e.Stream]; !ok {
			order = append(order, e.Stream)
		}

		byStream[e.Stream] = append(byStream[e.Stream], &logspb.LogRecord{
			TimeUnixNano:         uint64(e.Timestamp.UnixNano()),
			ObservedTimeUnixNano: uint64(observed.UnixNano()),
			Body:                 &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: e.Message}},
		})
	}

	req := &collogs.ExportLogsServiceRequest{}

	for _, stream := range order {
		req.ResourceLogs = append(req.ResourceLogs, &logspb.ResourceLogs{
			Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
				str("aws.region", s.Region),
				str("cloudwatch.log.group.name", s.LogGroup),
				str("cloudwatch.log.stream", stream),
			}},
			ScopeLogs: []*logspb.ScopeLogs{{LogRecords: byStream[stream]}},
		})
	}

	return req
}

// Emit sends events, retrying while the collector is not up yet.
func (s *OTLPSink) Emit(ctx context.Context, events []Event) error {
	body, err := proto.Marshal(s.Request(events, time.Now()))
	if err != nil {
		return err
	}

	delay := time.Second

	var last error

	for attempt := 0; attempt < s.Retries; attempt++ {
		if last = s.post(ctx, body); last == nil {
			return nil
		}

		if err := s.Sleep(ctx, delay); err != nil {
			return err
		}

		delay = min(delay*2, 30*time.Second)
	}

	return last
}

func (s *OTLPSink) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/x-protobuf")

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return err
	}

	defer func() { _ = resp.Body.Close() }()

	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("collector answered %s", resp.Status)
	}

	return nil
}
