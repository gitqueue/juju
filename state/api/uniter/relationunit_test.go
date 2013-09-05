// Copyright 2013 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package uniter_test

import (
	gc "launchpad.net/gocheck"

	"launchpad.net/juju-core/state"
	"launchpad.net/juju-core/state/api/params"
	"launchpad.net/juju-core/state/api/uniter"
	statetesting "launchpad.net/juju-core/state/testing"
)

// commonRelationSuiteMixin contains fields used by both relationSuite
// and relationUnitSuite. We're not just embeddnig relationUnitSuite
// into relationSuite to avoid running the former's tests twice.
type commonRelationSuiteMixin struct {
	mysqlMachine *state.Machine
	mysqlService *state.Service
	mysqlCharm   *state.Charm
	mysqlUnit    *state.Unit

	stateRelation *state.Relation
}

type relationUnitSuite struct {
	uniterSuite
	commonRelationSuiteMixin
}

var _ = gc.Suite(&relationUnitSuite{})

func (m *commonRelationSuiteMixin) SetUpTest(c *gc.C, s uniterSuite) {
	// Create another machine, service and unit, so we can
	// test relations and relation units.
	m.mysqlMachine, m.mysqlService, m.mysqlCharm, m.mysqlUnit = s.addMachineServiceCharmAndUnit(c, "mysql")

	// Add a relation, used by both this suite and relationSuite.
	m.stateRelation = s.addRelation(c, "wordpress", "mysql")
}

func (s *relationUnitSuite) SetUpTest(c *gc.C) {
	s.uniterSuite.SetUpTest(c)
	s.commonRelationSuiteMixin.SetUpTest(c, s.uniterSuite)
}

func (s *relationUnitSuite) TearDownTest(c *gc.C) {
	s.uniterSuite.TearDownTest(c)
}

func (s *relationUnitSuite) getRelationUnits(c *gc.C) (*state.RelationUnit, *uniter.RelationUnit) {
	wpRelUnit, err := s.stateRelation.Unit(s.wordpressUnit)
	c.Assert(err, gc.IsNil)
	apiRelation, err := s.uniter.Relation(s.stateRelation.Tag())
	c.Assert(err, gc.IsNil)
	apiUnit, err := s.uniter.Unit(s.wordpressUnit.Tag())
	c.Assert(err, gc.IsNil)
	apiRelUnit, err := apiRelation.Unit(apiUnit)
	c.Assert(err, gc.IsNil)
	return wpRelUnit, apiRelUnit
}

func (s *relationUnitSuite) TestEnterScope(c *gc.C) {
	// NOTE: This test is not as exhaustive as the ones in state.
	// Here, we just check the success case, while the two error
	// cases are tested separately.
	wpRelUnit, apiRelUnit := s.getRelationUnits(c)
	s.assertInScope(c, wpRelUnit, false)

	err := apiRelUnit.EnterScope()
	c.Assert(err, gc.IsNil)
	s.assertInScope(c, wpRelUnit, true)
}

func (s *relationUnitSuite) TestEnterScopeErrCannotEnterScope(c *gc.C) {
	// Test the ErrCannotEnterScope gets forwarded correctly.
	// We need to enter the scope wit the other unit first.
	myRelUnit, err := s.stateRelation.Unit(s.mysqlUnit)
	c.Assert(err, gc.IsNil)
	err = myRelUnit.EnterScope(nil)
	c.Assert(err, gc.IsNil)
	s.assertInScope(c, myRelUnit, true)
	// Now we destroy mysqlService, so the relation is be set to
	// dying.
	err = s.mysqlService.Destroy()
	c.Assert(err, gc.IsNil)
	err = s.stateRelation.Refresh()
	c.Assert(err, gc.IsNil)
	c.Assert(s.stateRelation.Life(), gc.Equals, state.Dying)
	// Enter the scope with wordpressUnit.
	wpRelUnit, apiRelUnit := s.getRelationUnits(c)
	s.assertInScope(c, wpRelUnit, false)
	err = apiRelUnit.EnterScope()
	c.Assert(err, gc.NotNil)
	c.Check(params.ErrCode(err), gc.Equals, params.CodeCannotEnterScope)
	c.Check(err, gc.ErrorMatches, "cannot enter scope: unit or relation is not alive")
}

func (s *relationUnitSuite) TestEnterScopeErrCannotEnterScopeYet(c *gc.C) {
}

func (s *relationUnitSuite) TestLeaveScope(c *gc.C) {
}

func (s *relationUnitSuite) TestWatchRelationUnits(c *gc.C) {
	// Enter scope with mysqlUnit.
	myRelUnit, err := s.stateRelation.Unit(s.mysqlUnit)
	c.Assert(err, gc.IsNil)
	err = myRelUnit.EnterScope(nil)
	c.Assert(err, gc.IsNil)
	s.assertInScope(c, myRelUnit, true)

	apiRel, err := s.uniter.Relation(s.stateRelation.Tag())
	c.Assert(err, gc.IsNil)
	apiUnit, err := s.uniter.Unit("unit-wordpress-0")
	c.Assert(err, gc.IsNil)
	apiRelUnit, err := apiRel.Unit(apiUnit)
	c.Assert(err, gc.IsNil)

	w, err := apiRelUnit.Watch()
	defer statetesting.AssertStop(c, w)
	wc := statetesting.NewRelationUnitsWatcherC(c, s.BackingState, w)

	// Initial event.
	wc.AssertChange([]string{"mysql/0"}, nil)

	// Leave scope with mysqlUnit, check it's detected.
	err = myRelUnit.LeaveScope()
	c.Assert(err, gc.IsNil)
	s.assertInScope(c, myRelUnit, false)
	wc.AssertChange(nil, []string{"mysql/0"})

	// Non-change is not reported.
	err = myRelUnit.LeaveScope()
	c.Assert(err, gc.IsNil)
	wc.AssertNoChange()

	// NOTE: This test is not as exhaustive as the one in state,
	// because the watcher is already tested there. Here we just
	// ensure we get the events when we expect them and don't get
	// them when they're not expected.

	statetesting.AssertStop(c, w)
	wc.AssertClosed()
}
