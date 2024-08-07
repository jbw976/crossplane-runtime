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
	"fmt"

	changelogs "github.com/crossplane/crossplane-runtime/apis/changelogs/proto/v1alpha1"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/reference"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"google.golang.org/protobuf/types/known/structpb"
)

// changeLogger processes changes to resources and helps to send them to the
// change log service
type changeLogger struct {
	enabled         bool
	client          changelogs.ChangeLogServiceClient
	providerVersion string
	log             logging.Logger
	record          event.Recorder
	snapshot        *structpb.Struct
}

// newChangeLogger creates a new changeLogger initialized with the given values
func newChangeLogger(enabled bool, client changelogs.ChangeLogServiceClient, log logging.Logger, record event.Recorder, providerVersion string) *changeLogger {
	return &changeLogger{
		enabled:         enabled,
		client:          client,
		providerVersion: providerVersion,
		log:             log,
		record:          record,
	}
}

// takeSnapshot attempts to take a snapshot of the given managed resource by
// converting it to a Struct that will be sent to the change logs after an
// external operation is performed, if the change logs feature is enabled. If
// there is an error during this process, an event will be recorded and no
// snapshot will be saved.
func (c *changeLogger) takeSnapshot(managed resource.Managed) {
	if !c.enabled {
		// change log feature isn't enabled, just return immediately
		return
	}

	// capture the full state of the managed resource from before we performed the change
	snapshot, err := resource.AsProtobufStruct(managed)
	if err != nil {
		c.log.Debug("Cannot snapshot managed resource", "error", err)
		c.record.Event(managed, event.Warning(reasonCannotSendChangeLog, err))
		return
	}

	c.snapshot = snapshot
}

// newChangeLogEntry creates a new change log entry with the given values. If
// change logs are not enabled then this function will return nil.
func (c *changeLogger) newChangeLogEntry(managed resource.Managed, opType changelogs.OperationType, changeErr error, ad AdditionalDetails) *changelogs.ChangeLogEntry {
	if !c.enabled {
		// change log feature isn't enabled, just return immediately
		return nil
	}

	// determine the full namespaced name of the managed resource
	var namespacedName string
	namespace := managed.GetNamespace()
	if namespace != "" {
		namespacedName = fmt.Sprintf("%s/%s", namespace, managed.GetName())
	} else {
		namespacedName = managed.GetName()
	}

	// get an error message from the error if it exists
	var changeErrMessage *string
	if changeErr != nil {
		changeErrMessage = reference.ToPtrValue(changeErr.Error())
	}

	gvk := managed.GetObjectKind().GroupVersionKind()

	return &changelogs.ChangeLogEntry{
		Provider:          c.providerVersion,
		ApiVersion:        gvk.GroupVersion().String(),
		Kind:              gvk.Kind,
		Name:              namespacedName,
		ExternalName:      meta.GetExternalName(managed),
		Operation:         opType,
		Snapshot:          c.snapshot,
		ErrorMessage:      changeErrMessage,
		AdditionalDetails: ad,
	}
}

// recordChangeLog sends the given change log entry to the change log service.
func (c *changeLogger) recordChangeLog(ctx context.Context, managed resource.Managed, entry *changelogs.ChangeLogEntry) {
	if !c.enabled {
		// change log feature isn't enabled, just return immediately
		return
	}

	// send everything we've got to the change log service
	cle := &changelogs.SendChangeLogRequest{Entry: entry}

	_, err := c.client.SendChangeLog(ctx, cle)
	if err != nil {
		c.log.Debug("Cannot send change log entry", "error", err)
		c.record.Event(managed, event.Warning(reasonCannotSendChangeLog, err))
	}
}
