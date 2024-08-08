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

	"github.com/crossplane/crossplane-runtime/apis/changelogs/proto/v1alpha1"
	"github.com/crossplane/crossplane-runtime/pkg/errors"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/reference"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
)

// ChangeLogger is an interface for recording changes made to resources to the
// change logs.
type ChangeLogger interface {
	RecordChangeLog(ctx context.Context, managed resource.Managed, opType v1alpha1.OperationType, changeErr error, ad AdditionalDetails) error
}

// GRPCChangeLogger processes changes to resources and helps to send them to the
// change log gRPC service.
type GRPCChangeLogger struct {
	client          v1alpha1.ChangeLogServiceClient
	providerVersion string
}

// NewGRPCChangeLogger creates a new gRPC based ChangeLogger initialized with
// the given values.
func NewGRPCChangeLogger(client v1alpha1.ChangeLogServiceClient, providerVersion string) *GRPCChangeLogger {
	return &GRPCChangeLogger{
		client:          client,
		providerVersion: providerVersion,
	}
}

// RecordChangeLog sends the given change log entry to the change log service.
func (g *GRPCChangeLogger) RecordChangeLog(ctx context.Context, managed resource.Managed, opType v1alpha1.OperationType, changeErr error, ad AdditionalDetails) error {
	// get an error message from the error if it exists
	var changeErrMessage *string
	if changeErr != nil {
		changeErrMessage = reference.ToPtrValue(changeErr.Error())
	}

	// capture the full state of the managed resource from before we performed the change
	snapshot, err := resource.AsProtobufStruct(managed)
	if err != nil {
		return errors.Wrap(err, "Cannot snapshot managed resource")
	}

	gvk := managed.GetObjectKind().GroupVersionKind()

	entry := &v1alpha1.ChangeLogEntry{
		Provider:          g.providerVersion,
		ApiVersion:        gvk.GroupVersion().String(),
		Kind:              gvk.Kind,
		Name:              managed.GetName(),
		ExternalName:      meta.GetExternalName(managed),
		Operation:         opType,
		Snapshot:          snapshot,
		ErrorMessage:      changeErrMessage,
		AdditionalDetails: ad,
	}

	// send everything we've got to the change log service
	_, err = g.client.SendChangeLog(ctx, &v1alpha1.SendChangeLogRequest{Entry: entry})
	return errors.Wrap(err, "Cannot send change log entry")
}

// nopChangeLogger does nothing for recording change logs, this is the default
// implementation if a provider has not enabled the change logs feature.
type nopChangeLogger struct{}

func newNopChangeLogger() *nopChangeLogger {
	return &nopChangeLogger{}
}

func (n *nopChangeLogger) RecordChangeLog(_ context.Context, _ resource.Managed, _ v1alpha1.OperationType, _ error, _ AdditionalDetails) error {
	return nil
}
