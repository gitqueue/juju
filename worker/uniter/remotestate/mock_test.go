// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package remotestate_test

import (
	"github.com/juju/names"
	"gopkg.in/juju/charm.v6-unstable"

	"github.com/juju/juju/api/watcher"
	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/state/multiwatcher"
	"github.com/juju/juju/worker/leadership"
	"github.com/juju/juju/worker/uniter/remotestate"
)

type mockNotifyWatcher struct {
	changes chan struct{}
	stopped bool
}

func (w *mockNotifyWatcher) Stop() error {
	w.stopped = true
	return nil
}

func (*mockNotifyWatcher) Err() error {
	return nil
}

func (w *mockNotifyWatcher) Changes() <-chan struct{} {
	return w.changes
}

type mockStringsWatcher struct {
	changes chan []string
	stopped bool
}

func (w *mockStringsWatcher) Stop() error {
	w.stopped = true
	return nil
}

func (*mockStringsWatcher) Err() error {
	return nil
}

func (w *mockStringsWatcher) Changes() <-chan []string {
	return w.changes
}

type mockRelationUnitsWatcher struct {
	changes chan multiwatcher.RelationUnitsChange
	stopped bool
}

func (w *mockRelationUnitsWatcher) Stop() error {
	w.stopped = true
	return nil
}

func (*mockRelationUnitsWatcher) Err() error {
	return nil
}

func (w *mockRelationUnitsWatcher) Changes() <-chan multiwatcher.RelationUnitsChange {
	return w.changes
}

type mockStorageAttachmentWatcher struct {
	changes chan struct{}
	stopped bool
}

func (w *mockStorageAttachmentWatcher) Stop() error {
	w.stopped = true
	return nil
}

func (*mockStorageAttachmentWatcher) Err() error {
	return nil
}

func (w *mockStorageAttachmentWatcher) Changes() <-chan struct{} {
	return w.changes
}

type mockState struct {
	unit                      mockUnit
	relations                 map[names.RelationTag]*mockRelation
	storageAttachment         map[params.StorageAttachmentId]params.StorageAttachment
	relationUnitsWatchers     map[names.RelationTag]*mockRelationUnitsWatcher
	storageAttachmentWatchers map[names.StorageTag]*mockStorageAttachmentWatcher
}

func (st *mockState) Relation(tag names.RelationTag) (remotestate.Relation, error) {
	r, ok := st.relations[tag]
	if !ok {
		return nil, &params.Error{Code: params.CodeNotFound}
	}
	return r, nil
}

func (st *mockState) StorageAttachment(
	storageTag names.StorageTag, unitTag names.UnitTag,
) (params.StorageAttachment, error) {
	if unitTag != st.unit.tag {
		return params.StorageAttachment{}, &params.Error{Code: params.CodeNotFound}
	}
	attachment, ok := st.storageAttachment[params.StorageAttachmentId{
		UnitTag:    unitTag.String(),
		StorageTag: storageTag.String(),
	}]
	if !ok {
		return params.StorageAttachment{}, &params.Error{Code: params.CodeNotFound}
	}
	if attachment.Kind == params.StorageKindUnknown {
		return params.StorageAttachment{}, &params.Error{Code: params.CodeNotProvisioned}
	}
	return attachment, nil
}

func (st *mockState) StorageAttachmentLife(
	ids []params.StorageAttachmentId,
) ([]params.LifeResult, error) {
	results := make([]params.LifeResult, len(ids))
	for i, id := range ids {
		attachment, ok := st.storageAttachment[id]
		if !ok {
			results[i] = params.LifeResult{
				Error: &params.Error{Code: params.CodeNotFound},
			}
			continue
		}
		results[i] = params.LifeResult{Life: attachment.Life}
	}
	return results, nil
}

func (st *mockState) Unit(tag names.UnitTag) (remotestate.Unit, error) {
	if tag != st.unit.tag {
		return nil, &params.Error{Code: params.CodeNotFound}
	}
	return &st.unit, nil
}

func (st *mockState) WatchRelationUnits(
	relationTag names.RelationTag, unitTag names.UnitTag,
) (watcher.RelationUnitsWatcher, error) {
	if unitTag != st.unit.tag {
		return nil, &params.Error{Code: params.CodeNotFound}
	}
	watcher, ok := st.relationUnitsWatchers[relationTag]
	if !ok {
		return nil, &params.Error{Code: params.CodeNotFound}
	}
	return watcher, nil
}

func (st *mockState) WatchStorageAttachment(
	storageTag names.StorageTag, unitTag names.UnitTag,
) (watcher.NotifyWatcher, error) {
	if unitTag != st.unit.tag {
		return nil, &params.Error{Code: params.CodeNotFound}
	}
	watcher, ok := st.storageAttachmentWatchers[storageTag]
	if !ok {
		return nil, &params.Error{Code: params.CodeNotFound}
	}
	return watcher, nil
}

type mockUnit struct {
	tag                   names.UnitTag
	life                  params.Life
	resolved              params.ResolvedMode
	service               mockService
	unitWatcher           mockNotifyWatcher
	addressesWatcher      mockNotifyWatcher
	configSettingsWatcher mockNotifyWatcher
	storageWatcher        mockStringsWatcher
	actionWatcher         mockStringsWatcher
}

func (u *mockUnit) Life() params.Life {
	return u.life
}

func (u *mockUnit) Refresh() error {
	return nil
}

func (u *mockUnit) Resolved() (params.ResolvedMode, error) {
	return u.resolved, nil
}

func (u *mockUnit) Service() (remotestate.Service, error) {
	return &u.service, nil
}

func (u *mockUnit) Tag() names.UnitTag {
	return u.tag
}

func (u *mockUnit) Watch() (watcher.NotifyWatcher, error) {
	return &u.unitWatcher, nil
}

func (u *mockUnit) WatchAddresses() (watcher.NotifyWatcher, error) {
	return &u.addressesWatcher, nil
}

func (u *mockUnit) WatchConfigSettings() (watcher.NotifyWatcher, error) {
	return &u.configSettingsWatcher, nil
}

func (u *mockUnit) WatchStorage() (watcher.StringsWatcher, error) {
	return &u.storageWatcher, nil
}

func (u *mockUnit) WatchActionNotifications() (watcher.StringsWatcher, error) {
	return &u.actionWatcher, nil
}

type mockService struct {
	tag                   names.ServiceTag
	life                  params.Life
	curl                  *charm.URL
	forceUpgrade          bool
	serviceWatcher        mockNotifyWatcher
	leaderSettingsWatcher mockNotifyWatcher
	relationsWatcher      mockStringsWatcher
}

func (s *mockService) CharmURL() (*charm.URL, bool, error) {
	return s.curl, s.forceUpgrade, nil
}

func (s *mockService) Life() params.Life {
	return s.life
}

func (s *mockService) Refresh() error {
	return nil
}

func (s *mockService) Tag() names.ServiceTag {
	return s.tag
}

func (s *mockService) Watch() (watcher.NotifyWatcher, error) {
	return &s.serviceWatcher, nil
}

func (s *mockService) WatchLeadershipSettings() (watcher.NotifyWatcher, error) {
	return &s.leaderSettingsWatcher, nil
}

func (s *mockService) WatchRelations() (watcher.StringsWatcher, error) {
	return &s.relationsWatcher, nil
}

type mockRelation struct {
	id   int
	life params.Life
}

func (r *mockRelation) Id() int {
	return r.id
}

func (r *mockRelation) Life() params.Life {
	return r.life
}

type mockLeadershipTracker struct {
	leadership.Tracker
	claimTicket  mockTicket
	leaderTicket mockTicket
	minionTicket mockTicket
}

func (mock *mockLeadershipTracker) ClaimLeader() leadership.Ticket {
	return &mock.claimTicket
}

func (mock *mockLeadershipTracker) WaitLeader() leadership.Ticket {
	return &mock.leaderTicket
}

func (mock *mockLeadershipTracker) WaitMinion() leadership.Ticket {
	return &mock.minionTicket
}

type mockTicket struct {
	ch     chan struct{}
	result bool
}

func (t *mockTicket) Ready() <-chan struct{} {
	return t.ch
}

func (t *mockTicket) Wait() bool {
	return t.result
}
