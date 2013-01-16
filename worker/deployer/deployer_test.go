package deployer_test

import (
	"sort"
	"strings"
	stdtesting "testing"
	"time"

	. "launchpad.net/gocheck"
	"launchpad.net/juju-core/juju/testing"
	"launchpad.net/juju-core/state"
	coretesting "launchpad.net/juju-core/testing"
	"launchpad.net/juju-core/worker/deployer"
)

func TestPackage(t *stdtesting.T) {
	coretesting.MgoTestPackage(t)
}

type DeployerSuite struct {
	testing.JujuConnSuite
	SimpleToolsFixture
}

var _ = Suite(&DeployerSuite{})

func (s *DeployerSuite) SetUpTest(c *C) {
	s.JujuConnSuite.SetUpTest(c)
	s.SimpleToolsFixture.SetUp(c, s.DataDir())
}

func (s *DeployerSuite) TearDownTest(c *C) {
	s.SimpleToolsFixture.TearDown(c)
	s.JujuConnSuite.TearDownTest(c)
}

func (s *DeployerSuite) TestDeployRecallRemovePrincipals(c *C) {
	// Create a machine, and a couple of units.
	m, err := s.State.AddMachine(state.JobHostUnits)
	c.Assert(err, IsNil)
	svc, err := s.State.AddService("wordpress", s.AddTestingCharm(c, "wordpress"))
	c.Assert(err, IsNil)
	u0, err := svc.AddUnit()
	c.Assert(err, IsNil)
	u1, err := svc.AddUnit()
	c.Assert(err, IsNil)

	// Create a deployer acting on behalf of the machine.
	mgr := s.getManager(c, m.EntityName())
	dep := deployer.NewDeployer(s.State, mgr, m.WatchPrincipalUnits())
	defer stop(c, dep)

	// Assign one unit, and wait for it to be deployed.
	err = u0.AssignToMachine(m)
	c.Assert(err, IsNil)
	s.waitFor(c, isDeployed(mgr, u0.Name()))

	// Assign another unit, and wait for that to be deployed.
	err = u1.AssignToMachine(m)
	c.Assert(err, IsNil)
	s.waitFor(c, isDeployed(mgr, u0.Name(), u1.Name()))

	// Cause a unit to become Dying, and check no change.
	err = u1.EnsureDying()
	c.Assert(err, IsNil)
	s.waitFor(c, isDeployed(mgr, u0.Name(), u1.Name()))

	// Cause a unit to become Dead, and check that it is both recalled and
	// removed from state.
	err = u0.EnsureDead()
	c.Assert(err, IsNil)
	s.waitFor(c, isRemoved(s.State, u0.Name()))
	s.waitFor(c, isDeployed(mgr, u1.Name()))

	// Remove the Dying unit from the machine, and check that it is recalled...
	err = u1.UnassignFromMachine()
	c.Assert(err, IsNil)
	s.waitFor(c, isDeployed(mgr))

	// ...and that the deployer, no longer bearing any responsibility for the
	// Dying unit, does nothing further to it.
	err = u1.Refresh()
	c.Assert(err, IsNil)
	c.Assert(u1.Life(), Equals, state.Dying)
}

func (s *DeployerSuite) TestRemoveNonAlivePrincipals(c *C) {
	// Create a machine, and a couple of units.
	m, err := s.State.AddMachine(state.JobHostUnits)
	c.Assert(err, IsNil)
	svc, err := s.State.AddService("wordpress", s.AddTestingCharm(c, "wordpress"))
	c.Assert(err, IsNil)
	u0, err := svc.AddUnit()
	c.Assert(err, IsNil)
	u1, err := svc.AddUnit()
	c.Assert(err, IsNil)

	// Assign the units to the machine, and set them to Dying/Dead.
	err = u0.AssignToMachine(m)
	c.Assert(err, IsNil)
	err = u0.EnsureDead()
	c.Assert(err, IsNil)
	err = u1.AssignToMachine(m)
	c.Assert(err, IsNil)
	err = u1.EnsureDying()
	c.Assert(err, IsNil)

	// When the deployer is started, in each case (1) no unit agent is deployed
	// and (2) the non-Alive unit is been removed from state.
	mgr := s.getManager(c, m.EntityName())
	dep := deployer.NewDeployer(s.State, mgr, m.WatchPrincipalUnits())
	defer stop(c, dep)
	s.waitFor(c, isRemoved(s.State, u0.Name()))
	s.waitFor(c, isRemoved(s.State, u1.Name()))
	s.waitFor(c, isDeployed(mgr))
}

func (s *DeployerSuite) prepareSubordinates(c *C) (*state.Unit, []*state.RelationUnit) {
	svc, err := s.State.AddService("wordpress", s.AddTestingCharm(c, "wordpress"))
	c.Assert(err, IsNil)
	u, err := svc.AddUnit()
	c.Assert(err, IsNil)
	rus := []*state.RelationUnit{}
	logging := s.AddTestingCharm(c, "logging")
	for _, name := range []string{"subsvc0", "subsvc1"} {
		_, err := s.State.AddService(name, logging)
		c.Assert(err, IsNil)
		eps, err := s.State.InferEndpoints([]string{"wordpress", name})
		c.Assert(err, IsNil)
		rel, err := s.State.AddRelation(eps...)
		c.Assert(err, IsNil)
		ru, err := rel.Unit(u)
		c.Assert(err, IsNil)
		rus = append(rus, ru)
	}
	return u, rus
}

func (s *DeployerSuite) TestDeployRecallRemoveSubordinates(c *C) {
	// Create a deployer acting on behalf of the principal.
	u, rus := s.prepareSubordinates(c)
	mgr := s.getManager(c, u.EntityName())
	dep := deployer.NewDeployer(s.State, mgr, u.WatchSubordinateUnits())
	defer stop(c, dep)

	// Add a subordinate, and wait for it to be deployed.
	err := rus[0].EnterScope(nil)
	c.Assert(err, IsNil)
	sub0, err := s.State.Unit("subsvc0/0")
	c.Assert(err, IsNil)
	s.waitFor(c, isDeployed(mgr, sub0.Name()))

	// And another.
	err = rus[1].EnterScope(nil)
	c.Assert(err, IsNil)
	sub1, err := s.State.Unit("subsvc1/0")
	c.Assert(err, IsNil)
	s.waitFor(c, isDeployed(mgr, sub0.Name(), sub1.Name()))

	// Set one to Dying; check nothing happens.
	err = sub1.EnsureDying()
	c.Assert(err, IsNil)
	s.State.StartSync()
	c.Assert(isRemoved(s.State, sub1.Name())(c), Equals, false)
	s.waitFor(c, isDeployed(mgr, sub0.Name(), sub1.Name()))

	// Set the other to Dead; check it's recalled and removed.
	err = sub0.EnsureDead()
	c.Assert(err, IsNil)
	s.waitFor(c, isDeployed(mgr, sub1.Name()))
	s.waitFor(c, isRemoved(s.State, sub0.Name()))
}

func (s *DeployerSuite) TestNonAliveSubordinates(c *C) {
	// Add two subordinate units and set them to Dead/Dying respectively.
	u, rus := s.prepareSubordinates(c)
	err := rus[0].EnterScope(nil)
	c.Assert(err, IsNil)
	sub0, err := s.State.Unit("subsvc0/0")
	c.Assert(err, IsNil)
	err = sub0.EnsureDead()
	c.Assert(err, IsNil)
	err = rus[1].EnterScope(nil)
	c.Assert(err, IsNil)
	sub1, err := s.State.Unit("subsvc1/0")
	c.Assert(err, IsNil)
	err = sub1.EnsureDying()
	c.Assert(err, IsNil)

	// When we start a new deployer, neither unit will be deployed and
	// both will be removed.
	mgr := s.getManager(c, u.EntityName())
	dep := deployer.NewDeployer(s.State, mgr, u.WatchSubordinateUnits())
	defer stop(c, dep)
	s.waitFor(c, isRemoved(s.State, sub0.Name()))
	s.waitFor(c, isRemoved(s.State, sub1.Name()))
}

func (s *DeployerSuite) waitFor(c *C, t func(c *C) bool) {
	s.State.StartSync()
	if t(c) {
		return
	}
	timeout := time.After(500 * time.Millisecond)
	for {
		select {
		case <-timeout:
			c.Fatalf("timeout")
		case <-time.After(50 * time.Millisecond):
			if t(c) {
				return
			}
		}
	}
	panic("unreachable")
}

func isDeployed(mgr deployer.Manager, expected ...string) func(*C) bool {
	return func(c *C) bool {
		sort.Strings(expected)
		current, err := mgr.DeployedUnits()
		c.Assert(err, IsNil)
		sort.Strings(current)
		return strings.Join(expected, ":") == strings.Join(current, ":")
	}
}

func isRemoved(st *state.State, name string) func(*C) bool {
	return func(c *C) bool {
		_, err := st.Unit(name)
		if state.IsNotFound(err) {
			return true
		}
		c.Assert(err, IsNil)
		return false
	}
}

type stopper interface {
	Stop() error
}

func stop(c *C, stopper stopper) {
	c.Assert(stopper.Stop(), IsNil)
}
