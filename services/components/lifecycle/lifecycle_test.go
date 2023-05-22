package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/forta-network/forta-core-go/protocol"
	mock_agentgrpc "github.com/forta-network/forta-node/clients/agentgrpc/mocks"
	mock_clients "github.com/forta-network/forta-node/clients/mocks"
	"github.com/forta-network/forta-node/config"
	"github.com/forta-network/forta-node/services/components/botio/botreq"
	mock_containers "github.com/forta-network/forta-node/services/components/containers/mocks"
	mock_metrics "github.com/forta-network/forta-node/services/components/metrics/mocks"
	mock_registry "github.com/forta-network/forta-node/services/components/registry/mocks"
	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

const (
	testBotID1      = "0x0100000000000000000000000000000000000000000000000000000000000000"
	testBotID2      = "0x0200000000000000000000000000000000000000000000000000000000000000"
	testImageRef    = "bafybeielvnt5apaxbk6chthc4dc3p6vscpx3ai4uvti7gwh253j7facsxu@sha256:e0e9efb6699b02750f6a9668084d37314f1de3a80da7e19c1d40da73ee57dd45"
	testContainerID = "test-container-id"
)

// LifecycleTestSuite composes type botLifecycleManager with a concrete type botPool
// and verifies that the bots will be managed as expected after assignment.
//
// This is different from bot pool and bot manager unit tests and acts as a component test
// which combines the two and verifies acceptance criteria in Given-When-Then style.
//
// The bot manager and the bot pool are expected to run in separate docker containers
// and stay connected via a mediator (see package mediator). This test avoids that complexity
// by making the bot manager call the concrete bot pool directly.
type LifecycleTestSuite struct {
	r *require.Assertions

	msgClient        *mock_clients.MockMessageClient
	lifecycleMetrics *mock_metrics.MockLifecycle
	botGrpc          *mock_agentgrpc.MockClient
	botRegistry      *mock_registry.MockBotRegistry
	botContainers    *mock_containers.MockBotClient
	dialer           *mock_agentgrpc.MockBotDialer

	resultChannels botreq.SendReceiveChannels

	botPool    *botPool
	botManager *botLifecycleManager

	suite.Suite
}

func (s *LifecycleTestSuite) log(msg string) {
	s.T().Log(msg)
}

func TestLifecycleTestSuite(t *testing.T) {
	suite.Run(t, &LifecycleTestSuite{})
}

func (s *LifecycleTestSuite) SetupTest() {
	s.r = s.Require()
	botRemoveTimeout = 0

	ctrl := gomock.NewController(s.T())
	s.msgClient = mock_clients.NewMockMessageClient(ctrl)
	s.lifecycleMetrics = mock_metrics.NewMockLifecycle(ctrl)
	s.botGrpc = mock_agentgrpc.NewMockClient(ctrl)
	s.botRegistry = mock_registry.NewMockBotRegistry(ctrl)
	s.botContainers = mock_containers.NewMockBotClient(ctrl)
	s.dialer = mock_agentgrpc.NewMockBotDialer(ctrl)
	s.resultChannels = botreq.MakeResultChannels()

	s.botPool = NewBotPool(context.Background(), s.msgClient, s.lifecycleMetrics, s.dialer, s.resultChannels.SendOnly(), 0)
	s.botPool.waitInit = true // hack to make testing synchronous
	s.botManager = NewManager(s.botRegistry, s.botContainers, s.botPool, s.lifecycleMetrics)
}

func (s *LifecycleTestSuite) TestDownloadTimeout() {
	s.log("should redownload a bot if downloading times out")

	assigned := []config.AgentConfig{
		{
			ID:    testBotID1,
			Image: testImageRef,
		},
	}

	// given that there is a new bot assignment
	// and no new assignments after the first time
	s.botRegistry.EXPECT().LoadAssignedBots().Return(assigned, nil).Times(2)

	// then the bot should be redownloaded, launched, dialed and initialized
	// upon download timeouts for the first time

	s.botContainers.EXPECT().EnsureBotImages(gomock.Any(), assigned).
		Return([]error{errors.New("download timeout")}).Times(1)
	s.lifecycleMetrics.EXPECT().FailurePull(assigned[0]).Times(1)
	s.lifecycleMetrics.EXPECT().StatusRunning().Times(1) // not bots running due to download failure

	s.botContainers.EXPECT().EnsureBotImages(gomock.Any(), assigned).
		Return([]error{nil}).Times(1)
	s.botContainers.EXPECT().LaunchBot(gomock.Any(), assigned[0]).Return(nil).Times(1)
	s.lifecycleMetrics.EXPECT().StatusRunning(assigned[0]).Times(1) // bot is running

	s.lifecycleMetrics.EXPECT().Start(assigned[0]).Times(1)
	s.lifecycleMetrics.EXPECT().StatusAttached(assigned[0]).Times(1)
	s.lifecycleMetrics.EXPECT().StatusInitialized(assigned[0]).Times(1)
	s.dialer.EXPECT().DialBot(assigned[0]).Return(s.botGrpc, nil).Times(1)
	s.botGrpc.EXPECT().Initialize(gomock.Any(), gomock.Any()).Return(&protocol.InitializeResponse{}, nil).
		Times(1)

	// when the bot manager manages the assigned bots over time
	s.r.NoError(s.botManager.ManageBots(context.Background()))
	s.r.NoError(s.botManager.ManageBots(context.Background()))
}

func (s *LifecycleTestSuite) TestLaunchFailure() {
	s.log("should relaunch a bot if launching fails")

	assigned := []config.AgentConfig{
		{
			ID:    testBotID1,
			Image: testImageRef,
		},
	}

	// given that there is a new bot assignment
	// and no new assignments after the first time
	s.botRegistry.EXPECT().LoadAssignedBots().Return(assigned, nil).Times(2)

	// then the bot should be relaunched, dialed and initialized
	// upon launch failure for the first time

	s.botContainers.EXPECT().EnsureBotImages(gomock.Any(), assigned).
		Return([]error{nil}).Times(1)
	s.botContainers.EXPECT().LaunchBot(gomock.Any(), assigned[0]).Return(errors.New("failed to launch")).Times(1)
	s.lifecycleMetrics.EXPECT().FailureLaunch(assigned[0]).Times(1)
	s.lifecycleMetrics.EXPECT().StatusRunning().Times(1) // not bots running due to download failure

	s.botContainers.EXPECT().EnsureBotImages(gomock.Any(), assigned).
		Return([]error{nil}).Times(1)
	s.botContainers.EXPECT().LaunchBot(gomock.Any(), assigned[0]).Return(nil).Times(1)
	s.lifecycleMetrics.EXPECT().StatusRunning(assigned[0]).Times(1) // bot is running

	s.lifecycleMetrics.EXPECT().Start(assigned[0]).Times(1)
	s.lifecycleMetrics.EXPECT().StatusAttached(assigned[0]).Times(1)
	s.lifecycleMetrics.EXPECT().StatusInitialized(assigned[0]).Times(1)
	s.dialer.EXPECT().DialBot(assigned[0]).Return(s.botGrpc, nil).Times(1)
	s.botGrpc.EXPECT().Initialize(gomock.Any(), gomock.Any()).Return(&protocol.InitializeResponse{}, nil).
		Times(1)

	// when the bot manager manages the assigned bots over time
	s.r.NoError(s.botManager.ManageBots(context.Background()))
	s.r.NoError(s.botManager.ManageBots(context.Background()))
}

func (s *LifecycleTestSuite) TestDialFailure() {
	s.log("should not reload or redial a bot if dialing finally fails")

	assigned := []config.AgentConfig{
		{
			ID:    testBotID1,
			Image: testImageRef,
		},
	}

	// given that there is a new bot assignment
	// and no new assignments after the first time
	s.botRegistry.EXPECT().LoadAssignedBots().Return(assigned, nil).Times(2)

	// then there should be no reloading and redialing upon dialing failures
	s.botContainers.EXPECT().EnsureBotImages(gomock.Any(), assigned).Return([]error{nil}).Times(1)
	s.botContainers.EXPECT().LaunchBot(gomock.Any(), assigned[0]).Return(nil).Times(1)
	s.lifecycleMetrics.EXPECT().StatusRunning(assigned[0]).Times(2)
	s.lifecycleMetrics.EXPECT().Start(assigned[0]).Times(1)
	s.dialer.EXPECT().DialBot(assigned[0]).Return(nil, errors.New("failed to dial")).Times(1)

	// when the bot manager manages the assigned bots over time
	s.r.NoError(s.botManager.ManageBots(context.Background()))
	s.r.NoError(s.botManager.ManageBots(context.Background()))
}

func (s *LifecycleTestSuite) TestInitializeFailure() {
	s.log("should not reload or reinitialize a bot if initialization finally fails")

	assigned := []config.AgentConfig{
		{
			ID:    testBotID1,
			Image: testImageRef,
		},
	}

	// given that there is a new bot assignment
	// and no new assignments after the first time
	s.botRegistry.EXPECT().LoadAssignedBots().Return(assigned, nil).Times(2)

	// then there should be no reloading and redialing upon initialization failures
	s.botContainers.EXPECT().EnsureBotImages(gomock.Any(), assigned).Return([]error{nil}).Times(1)
	s.botContainers.EXPECT().LaunchBot(gomock.Any(), assigned[0]).Return(nil).Times(1)
	s.lifecycleMetrics.EXPECT().StatusRunning(assigned[0]).Times(2)
	s.lifecycleMetrics.EXPECT().Start(assigned[0]).Times(1)
	s.lifecycleMetrics.EXPECT().StatusAttached(assigned[0]).Times(1)
	s.lifecycleMetrics.EXPECT().FailureInitialize(assigned[0]).Times(1)
	s.dialer.EXPECT().DialBot(assigned[0]).Return(s.botGrpc, nil).Times(1)
	s.botGrpc.EXPECT().Initialize(gomock.Any(), gomock.Any()).Return(nil, errors.New("failed to init")).Times(1)

	// when the bot manager manages the assigned bots over time
	s.r.NoError(s.botManager.ManageBots(context.Background()))
	s.r.NoError(s.botManager.ManageBots(context.Background()))
}

func (s *LifecycleTestSuite) TestExitedRestarted() {
	s.log("should restart, redial and reinitialize exited bots")

	assigned := []config.AgentConfig{
		{
			ID:    testBotID1,
			Image: testImageRef,
		},
	}

	// given that there is a new bot assignment
	// and no new assignments after the first time
	s.botRegistry.EXPECT().LoadAssignedBots().Return(assigned, nil).Times(2)

	// then there should be restart and reinitialization

	s.botContainers.EXPECT().EnsureBotImages(gomock.Any(), assigned).Return([]error{nil}).Times(1)
	s.botContainers.EXPECT().LaunchBot(gomock.Any(), assigned[0]).Return(nil).Times(1)
	s.lifecycleMetrics.EXPECT().StatusRunning(assigned[0]).Times(2)
	s.lifecycleMetrics.EXPECT().Start(assigned[0]).Times(2)
	s.lifecycleMetrics.EXPECT().StatusAttached(assigned[0]).Times(2)
	s.lifecycleMetrics.EXPECT().StatusInitialized(assigned[0]).Times(2)
	s.dialer.EXPECT().DialBot(assigned[0]).Return(s.botGrpc, nil).Times(2)
	s.botGrpc.EXPECT().Initialize(gomock.Any(), gomock.Any()).Return(&protocol.InitializeResponse{}, nil).
		Times(2)

	dockerContainerName := fmt.Sprintf("/%s", assigned[0].ContainerName())

	s.botContainers.EXPECT().LoadBotContainers(gomock.Any()).Return([]types.Container{
		{
			ID:    testContainerID,
			Names: []string{dockerContainerName},
			State: "running",
		},
	}, nil).Times(1)
	s.botContainers.EXPECT().LoadBotContainers(gomock.Any()).Return([]types.Container{
		{
			ID:    testContainerID,
			Names: []string{dockerContainerName},
			State: "exited",
		},
	}, nil).Times(1)

	s.lifecycleMetrics.EXPECT().ActionRestart(assigned[0])
	s.botContainers.EXPECT().StartWaitBotContainer(gomock.Any(), testContainerID).Return(nil)

	// when the bot manager manages the assigned bots over time
	s.r.NoError(s.botManager.ManageBots(context.Background()))
	s.r.NoError(s.botManager.RestartExitedBots(context.Background()))
	s.r.NoError(s.botManager.ManageBots(context.Background()))
	s.r.NoError(s.botManager.RestartExitedBots(context.Background()))
}

func (s *LifecycleTestSuite) TestUnassigned() {
	s.log("should tear down unassigned bots")

	assigned := []config.AgentConfig{
		{
			ID:    testBotID1,
			Image: testImageRef,
		},
	}

	// given that there is a new bot assignment
	s.botRegistry.EXPECT().LoadAssignedBots().Return(assigned, nil).Times(1)
	// and the assignment is removed shortly
	s.botRegistry.EXPECT().LoadAssignedBots().Return(nil, nil).Times(1)

	// then the bot should be started
	s.botContainers.EXPECT().EnsureBotImages(gomock.Any(), assigned).Return([]error{nil}).Times(1)
	s.botContainers.EXPECT().LaunchBot(gomock.Any(), assigned[0]).Return(nil).Times(1)
	s.lifecycleMetrics.EXPECT().StatusRunning(assigned[0]).Times(1)
	s.lifecycleMetrics.EXPECT().Start(assigned[0]).Times(1)
	s.lifecycleMetrics.EXPECT().StatusAttached(assigned[0]).Times(1)
	s.lifecycleMetrics.EXPECT().StatusInitialized(assigned[0]).Times(1)
	s.dialer.EXPECT().DialBot(assigned[0]).Return(s.botGrpc, nil).Times(1)
	s.botGrpc.EXPECT().Initialize(gomock.Any(), gomock.Any()).Return(&protocol.InitializeResponse{}, nil).
		Times(1)

	// and should shortly be tore down
	s.lifecycleMetrics.EXPECT().StatusStopping(assigned[0])
	s.botContainers.EXPECT().TearDownBot(gomock.Any(), assigned[0]).Return(nil)
	s.lifecycleMetrics.EXPECT().StatusRunning().Times(1)

	// when the bot manager manages the assigned bots over time
	s.r.NoError(s.botManager.ManageBots(context.Background()))
	s.r.NoError(s.botManager.ManageBots(context.Background()))
}

func (s *LifecycleTestSuite) TestConfigUpdated() {
	s.log("should update bot config without tearing down")

	assigned := []config.AgentConfig{
		{
			ID:    testBotID1,
			Image: testImageRef,
		},
	}

	// given that there is a new bot assignment
	s.botRegistry.EXPECT().LoadAssignedBots().Return(assigned, nil).Times(1)
	// and the assignment is removed shortly
	s.botRegistry.EXPECT().LoadAssignedBots().Return(nil, nil).Times(1)

	// then the bot should be tore down

	s.botContainers.EXPECT().EnsureBotImages(gomock.Any(), assigned).Return([]error{nil}).Times(1)
	s.botContainers.EXPECT().LaunchBot(gomock.Any(), assigned[0]).Return(nil).Times(1)
	s.lifecycleMetrics.EXPECT().StatusRunning(assigned[0]).Times(1)
	s.lifecycleMetrics.EXPECT().Start(assigned[0]).Times(1)
	s.lifecycleMetrics.EXPECT().StatusAttached(assigned[0]).Times(1)
	s.lifecycleMetrics.EXPECT().StatusInitialized(assigned[0]).Times(1)
	s.dialer.EXPECT().DialBot(assigned[0]).Return(s.botGrpc, nil).Times(1)
	s.botGrpc.EXPECT().Initialize(gomock.Any(), gomock.Any()).Return(&protocol.InitializeResponse{}, nil).
		Times(1)

	s.lifecycleMetrics.EXPECT().StatusStopping(assigned[0])
	s.botContainers.EXPECT().TearDownBot(gomock.Any(), assigned[0]).Return(nil)
	s.lifecycleMetrics.EXPECT().StatusRunning().Times(1)

	// when the bot manager manages the assigned bots over time
	s.r.NoError(s.botManager.ManageBots(context.Background()))
	s.r.NoError(s.botManager.ManageBots(context.Background()))
}