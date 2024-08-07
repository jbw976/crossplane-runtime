/*
Copyright 2024 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package managed

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/structpb"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	changelogs "github.com/crossplane/crossplane-runtime/apis/changelogs/proto/v1alpha1"
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/reference"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/crossplane/crossplane-runtime/pkg/resource/fake"
)

// A mock implementation of the ChangeLogServiceClient interface to help with
// testing and verifying change log entries.
type changeLogServiceClient struct {
	enable   bool
	requests []*changelogs.SendChangeLogRequest
	sendFn   func(ctx context.Context, in *changelogs.SendChangeLogRequest, opts ...grpc.CallOption) (*changelogs.SendChangeLogResponse, error)
}

func (c *changeLogServiceClient) SendChangeLog(ctx context.Context, in *changelogs.SendChangeLogRequest, opts ...grpc.CallOption) (*changelogs.SendChangeLogResponse, error) {
	c.requests = append(c.requests, in)
	if c.sendFn != nil {
		return c.sendFn(ctx, in, opts...)
	}
	return nil, nil
}

func TestChangeLogger(t *testing.T) {
	type args struct {
		mr  resource.Managed
		ad  AdditionalDetails
		err error
		c   *changeLogServiceClient
	}

	type want struct {
		requests []*changelogs.SendChangeLogRequest
		events   []event.Event
	}

	errBoom := errors.New("boom")

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"ChangeLogsNotEnabled": {
			reason: "No change logs should be recorded when change logs are not enabled.",
			args: args{
				// disable change logs for this test case
				c: &changeLogServiceClient{enable: false, requests: nil},
			},
			want: want{
				requests: nil, // no change logs should be sent
			},
		},
		"ChangeLogsEnabled": {
			reason: "Change log entry should be recorded when change logs are enabled.",
			args: args{
				mr: &fake.Managed{ObjectMeta: metav1.ObjectMeta{
					Name:        "cool-managed",
					Annotations: map[string]string{meta.AnnotationKeyExternalName: "cool-managed"},
				}},
				err: errBoom,
				ad:  AdditionalDetails{"key": "value", "key2": "value2"},
				c:   &changeLogServiceClient{enable: true, requests: []*changelogs.SendChangeLogRequest{}},
			},
			want: want{
				// a well fleshed out change log entry should be sent
				requests: []*changelogs.SendChangeLogRequest{
					{
						Entry: &changelogs.ChangeLogEntry{
							Provider:     "provider-cool:v9.99.999",
							ApiVersion:   (&fake.Managed{}).GetObjectKind().GroupVersionKind().GroupVersion().String(),
							Kind:         (&fake.Managed{}).GetObjectKind().GroupVersionKind().Kind,
							Name:         "cool-managed",
							ExternalName: "cool-managed",
							Operation:    changelogs.OperationType_OPERATION_TYPE_CREATE,
							Snapshot: mustObjectAsProtobufStruct(&fake.Managed{ObjectMeta: metav1.ObjectMeta{
								Name:        "cool-managed",
								Annotations: map[string]string{meta.AnnotationKeyExternalName: "cool-managed"},
							}}),
							ErrorMessage:      reference.ToPtrValue("boom"),
							AdditionalDetails: AdditionalDetails{"key": "value", "key2": "value2"},
						},
					},
				},
			},
		},
		"SendChangeLogsFailure": {
			reason: "Error from sending change log entry should be handled and recorded.",
			args: args{
				mr: &fake.Managed{},
				c: &changeLogServiceClient{
					enable:   true,
					requests: []*changelogs.SendChangeLogRequest{},
					// make the send change log function return an error
					sendFn: func(_ context.Context, _ *changelogs.SendChangeLogRequest, _ ...grpc.CallOption) (*changelogs.SendChangeLogResponse, error) {
						return &changelogs.SendChangeLogResponse{}, errBoom
					},
				},
			},
			want: want{
				// we'll still see a change log entry, but it won't make it all
				// the way to its destination and we should see an event for
				// that failure
				requests: []*changelogs.SendChangeLogRequest{
					{
						Entry: &changelogs.ChangeLogEntry{
							// we expect less fields to be set on the change log
							// entry because we're not initializing the managed
							// resource with much data in this simulated failure
							// test case
							Provider:   "provider-cool:v9.99.999",
							ApiVersion: (&fake.Managed{}).GetObjectKind().GroupVersionKind().GroupVersion().String(),
							Kind:       (&fake.Managed{}).GetObjectKind().GroupVersionKind().Kind,
							Operation:  changelogs.OperationType_OPERATION_TYPE_CREATE,
							Snapshot:   mustObjectAsProtobufStruct(&fake.Managed{}),
						},
					},
				},
				events: []event.Event{
					{
						// we do expect an event to be recorded for the send failure
						Type:        event.TypeWarning,
						Reason:      reasonCannotSendChangeLog,
						Message:     errBoom.Error(),
						Annotations: map[string]string{},
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			logger := logging.NewNopLogger()
			recorder := newMockRecorder()

			// create the change logger given our test cases's inputs, then take
			// a snapshot and record the change log entry
			changeLogger := newChangeLogger(tc.args.c.enable, tc.args.c, logger, recorder, "provider-cool:v9.99.999")
			changeLogger.takeSnapshot(tc.args.mr)
			entry := changeLogger.newChangeLogEntry(tc.args.mr, changelogs.OperationType_OPERATION_TYPE_CREATE, tc.args.err, tc.args.ad)
			changeLogger.recordChangeLog(context.Background(), tc.args.mr, entry)

			// we ignore unexported fields in the protobuf related types, we
			// don't care much for the internals that cmp doesn't handle
			// well. The exported fields are good enough.
			ignoreUnexported := cmpopts.IgnoreUnexported(
				changelogs.SendChangeLogRequest{},
				changelogs.ChangeLogEntry{},
				structpb.Struct{},
				structpb.Value{})

			if diff := cmp.Diff(tc.want.requests, tc.args.c.requests, ignoreUnexported); diff != "" {
				t.Errorf("\nReason: %s\nr.recordChangeLog(...): -want requests, +got requests:\n%s", tc.reason, diff)
			}

			if diff := cmp.Diff(tc.want.events, recorder.events); diff != "" {
				t.Errorf("\nReason: %s\nr.recordChangeLog(...): -want events, +got events:\n%s", tc.reason, diff)
			}
		})
	}
}

// mockRecorder will record events seen during the test cases.
type mockRecorder struct {
	events []event.Event
}

func newMockRecorder() *mockRecorder {
	return &mockRecorder{}
}

func (r *mockRecorder) Event(_ runtime.Object, e event.Event) {
	r.events = append(r.events, e)
}

func (r *mockRecorder) WithAnnotations(_ ...string) event.Recorder { return r }

func mustObjectAsProtobufStruct(o runtime.Object) *structpb.Struct {
	s, err := resource.AsProtobufStruct(o)
	if err != nil {
		panic(err)
	}
	return s
}
