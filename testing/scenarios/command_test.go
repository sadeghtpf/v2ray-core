package scenarios

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/grpc"

	"v2ray.com/core"
	"v2ray.com/core/app/commander"
	"v2ray.com/core/app/policy"
	"v2ray.com/core/app/proxyman"
	"v2ray.com/core/app/proxyman/command"
	"v2ray.com/core/app/router"
	"v2ray.com/core/app/stats"
	statscmd "v2ray.com/core/app/stats/command"
	"v2ray.com/core/common"
	"v2ray.com/core/common/net"
	"v2ray.com/core/common/protocol"
	"v2ray.com/core/common/serial"
	"v2ray.com/core/common/uuid"
	"v2ray.com/core/proxy/dokodemo"
	"v2ray.com/core/proxy/freedom"
	"v2ray.com/core/proxy/vmess"
	"v2ray.com/core/proxy/vmess/inbound"
	"v2ray.com/core/proxy/vmess/outbound"
	"v2ray.com/core/testing/servers/tcp"
	. "v2ray.com/ext/assert"
)

func TestCommanderRemoveHandler(t *testing.T) {
	assert := With(t)

	tcpServer := tcp.Server{
		MsgProcessor: xor,
	}
	dest, err := tcpServer.Start()
	common.Must(err)
	defer tcpServer.Close()

	clientPort := tcp.PickPort()
	cmdPort := tcp.PickPort()
	clientConfig := &core.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(&commander.Config{
				Tag: "api",
				Service: []*serial.TypedMessage{
					serial.ToTypedMessage(&command.Config{}),
				},
			}),
			serial.ToTypedMessage(&router.Config{
				Rule: []*router.RoutingRule{
					{
						InboundTag: []string{"api"},
						TargetTag: &router.RoutingRule_Tag{
							Tag: "api",
						},
					},
				},
			}),
		},
		Inbound: []*core.InboundHandlerConfig{
			{
				Tag: "d",
				ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
					PortRange: net.SinglePortRange(clientPort),
					Listen:    net.NewIPOrDomain(net.LocalHostIP),
				}),
				ProxySettings: serial.ToTypedMessage(&dokodemo.Config{
					Address:  net.NewIPOrDomain(dest.Address),
					Port:     uint32(dest.Port),
					Networks: []net.Network{net.Network_TCP},
				}),
			},
			{
				Tag: "api",
				ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
					PortRange: net.SinglePortRange(cmdPort),
					Listen:    net.NewIPOrDomain(net.LocalHostIP),
				}),
				ProxySettings: serial.ToTypedMessage(&dokodemo.Config{
					Address:  net.NewIPOrDomain(dest.Address),
					Port:     uint32(dest.Port),
					Networks: []net.Network{net.Network_TCP},
				}),
			},
		},
		Outbound: []*core.OutboundHandlerConfig{
			{
				Tag:           "default-outbound",
				ProxySettings: serial.ToTypedMessage(&freedom.Config{}),
			},
		},
	}

	servers, err := InitializeServerConfigs(clientConfig)
	common.Must(err)

	defer CloseAllServers(servers)

	{
		conn, err := net.DialTCP("tcp", nil, &net.TCPAddr{
			IP:   []byte{127, 0, 0, 1},
			Port: int(clientPort),
		})
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close() // nolint: errcheck

		payload := "commander request."
		nBytes, err := conn.Write([]byte(payload))
		common.Must(err)
		assert(nBytes, Equals, len(payload))

		response := make([]byte, 1024)
		nBytes, err = conn.Read(response)
		common.Must(err)
		if r := cmp.Diff(response[:nBytes], xor([]byte(payload))); r != "" {
			t.Fatal(r)
		}
	}

	cmdConn, err := grpc.Dial(fmt.Sprintf("127.0.0.1:%d", cmdPort), grpc.WithInsecure(), grpc.WithBlock())
	common.Must(err)
	defer cmdConn.Close()

	hsClient := command.NewHandlerServiceClient(cmdConn)
	resp, err := hsClient.RemoveInbound(context.Background(), &command.RemoveInboundRequest{
		Tag: "d",
	})
	common.Must(err)
	assert(resp, IsNotNil)

	{
		_, err := net.DialTCP("tcp", nil, &net.TCPAddr{
			IP:   []byte{127, 0, 0, 1},
			Port: int(clientPort),
		})
		assert(err, IsNotNil)
	}
}

func TestCommanderAddRemoveUser(t *testing.T) {
	assert := With(t)

	tcpServer := tcp.Server{
		MsgProcessor: xor,
	}
	dest, err := tcpServer.Start()
	common.Must(err)
	defer tcpServer.Close()

	u1 := protocol.NewID(uuid.New())
	u2 := protocol.NewID(uuid.New())

	cmdPort := tcp.PickPort()
	serverPort := tcp.PickPort()
	serverConfig := &core.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(&commander.Config{
				Tag: "api",
				Service: []*serial.TypedMessage{
					serial.ToTypedMessage(&command.Config{}),
				},
			}),
			serial.ToTypedMessage(&router.Config{
				Rule: []*router.RoutingRule{
					{
						InboundTag: []string{"api"},
						TargetTag: &router.RoutingRule_Tag{
							Tag: "api",
						},
					},
				},
			}),
			serial.ToTypedMessage(&policy.Config{
				Level: map[uint32]*policy.Policy{
					0: {
						Timeout: &policy.Policy_Timeout{
							UplinkOnly:   &policy.Second{Value: 0},
							DownlinkOnly: &policy.Second{Value: 0},
						},
					},
				},
			}),
		},
		Inbound: []*core.InboundHandlerConfig{
			{
				Tag: "v",
				ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
					PortRange: net.SinglePortRange(serverPort),
					Listen:    net.NewIPOrDomain(net.LocalHostIP),
				}),
				ProxySettings: serial.ToTypedMessage(&inbound.Config{
					User: []*protocol.User{
						{
							Account: serial.ToTypedMessage(&vmess.Account{
								Id:      u1.String(),
								AlterId: 64,
							}),
						},
					},
				}),
			},
			{
				Tag: "api",
				ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
					PortRange: net.SinglePortRange(cmdPort),
					Listen:    net.NewIPOrDomain(net.LocalHostIP),
				}),
				ProxySettings: serial.ToTypedMessage(&dokodemo.Config{
					Address:  net.NewIPOrDomain(dest.Address),
					Port:     uint32(dest.Port),
					Networks: []net.Network{net.Network_TCP},
				}),
			},
		},
		Outbound: []*core.OutboundHandlerConfig{
			{
				ProxySettings: serial.ToTypedMessage(&freedom.Config{}),
			},
		},
	}

	clientPort := tcp.PickPort()
	clientConfig := &core.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(&policy.Config{
				Level: map[uint32]*policy.Policy{
					0: {
						Timeout: &policy.Policy_Timeout{
							UplinkOnly:   &policy.Second{Value: 0},
							DownlinkOnly: &policy.Second{Value: 0},
						},
					},
				},
			}),
		},
		Inbound: []*core.InboundHandlerConfig{
			{
				Tag: "d",
				ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
					PortRange: net.SinglePortRange(clientPort),
					Listen:    net.NewIPOrDomain(net.LocalHostIP),
				}),
				ProxySettings: serial.ToTypedMessage(&dokodemo.Config{
					Address: net.NewIPOrDomain(dest.Address),
					Port:    uint32(dest.Port),
					NetworkList: &net.NetworkList{
						Network: []net.Network{net.Network_TCP},
					},
				}),
			},
		},
		Outbound: []*core.OutboundHandlerConfig{
			{
				ProxySettings: serial.ToTypedMessage(&outbound.Config{
					Receiver: []*protocol.ServerEndpoint{
						{
							Address: net.NewIPOrDomain(net.LocalHostIP),
							Port:    uint32(serverPort),
							User: []*protocol.User{
								{
									Account: serial.ToTypedMessage(&vmess.Account{
										Id:      u2.String(),
										AlterId: 64,
										SecuritySettings: &protocol.SecurityConfig{
											Type: protocol.SecurityType_AES128_GCM,
										},
									}),
								},
							},
						},
					},
				}),
			},
		},
	}

	servers, err := InitializeServerConfigs(serverConfig, clientConfig)
	common.Must(err)

	{
		conn, err := net.DialTCP("tcp", nil, &net.TCPAddr{
			IP:   []byte{127, 0, 0, 1},
			Port: int(clientPort),
		})
		common.Must(err)

		payload := "commander request."
		nBytes, err := conn.Write([]byte(payload))
		common.Must(err)
		assert(nBytes, Equals, len(payload))

		response := make([]byte, 1024)
		nBytes, err = conn.Read(response)
		assert(nBytes, Equals, 0)
		assert(err, Equals, io.EOF)
		assert(conn.Close(), IsNil)
	}

	cmdConn, err := grpc.Dial(fmt.Sprintf("127.0.0.1:%d", cmdPort), grpc.WithInsecure(), grpc.WithBlock())
	common.Must(err)
	defer cmdConn.Close()

	hsClient := command.NewHandlerServiceClient(cmdConn)
	resp, err := hsClient.AlterInbound(context.Background(), &command.AlterInboundRequest{
		Tag: "v",
		Operation: serial.ToTypedMessage(
			&command.AddUserOperation{
				User: &protocol.User{
					Email: "test@v2ray.com",
					Account: serial.ToTypedMessage(&vmess.Account{
						Id:      u2.String(),
						AlterId: 64,
					}),
				},
			}),
	})
	common.Must(err)
	assert(resp, IsNotNil)

	{
		conn, err := net.DialTCP("tcp", nil, &net.TCPAddr{
			IP:   []byte{127, 0, 0, 1},
			Port: int(clientPort),
		})
		common.Must(err)

		payload := "commander request."
		nBytes, err := conn.Write([]byte(payload))
		common.Must(err)
		assert(nBytes, Equals, len(payload))

		response := make([]byte, 1024)
		nBytes, err = conn.Read(response)
		common.Must(err)
		assert(response[:nBytes], Equals, xor([]byte(payload)))
		assert(conn.Close(), IsNil)
	}

	resp, err = hsClient.AlterInbound(context.Background(), &command.AlterInboundRequest{
		Tag:       "v",
		Operation: serial.ToTypedMessage(&command.RemoveUserOperation{Email: "test@v2ray.com"}),
	})
	assert(resp, IsNotNil)
	common.Must(err)

	CloseAllServers(servers)
}

func TestCommanderStats(t *testing.T) {
	assert := With(t)

	tcpServer := tcp.Server{
		MsgProcessor: xor,
	}
	dest, err := tcpServer.Start()
	common.Must(err)
	defer tcpServer.Close()

	userID := protocol.NewID(uuid.New())
	serverPort := tcp.PickPort()
	cmdPort := tcp.PickPort()

	serverConfig := &core.Config{
		App: []*serial.TypedMessage{
			serial.ToTypedMessage(&stats.Config{}),
			serial.ToTypedMessage(&commander.Config{
				Tag: "api",
				Service: []*serial.TypedMessage{
					serial.ToTypedMessage(&statscmd.Config{}),
				},
			}),
			serial.ToTypedMessage(&router.Config{
				Rule: []*router.RoutingRule{
					{
						InboundTag: []string{"api"},
						TargetTag: &router.RoutingRule_Tag{
							Tag: "api",
						},
					},
				},
			}),
			serial.ToTypedMessage(&policy.Config{
				Level: map[uint32]*policy.Policy{
					0: {
						Timeout: &policy.Policy_Timeout{
							UplinkOnly:   &policy.Second{Value: 0},
							DownlinkOnly: &policy.Second{Value: 0},
						},
					},
					1: {
						Stats: &policy.Policy_Stats{
							UserUplink:   true,
							UserDownlink: true,
						},
					},
				},
				System: &policy.SystemPolicy{
					Stats: &policy.SystemPolicy_Stats{
						InboundUplink: true,
					},
				},
			}),
		},
		Inbound: []*core.InboundHandlerConfig{
			{
				Tag: "vmess",
				ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
					PortRange: net.SinglePortRange(serverPort),
					Listen:    net.NewIPOrDomain(net.LocalHostIP),
				}),
				ProxySettings: serial.ToTypedMessage(&inbound.Config{
					User: []*protocol.User{
						{
							Level: 1,
							Email: "test",
							Account: serial.ToTypedMessage(&vmess.Account{
								Id:      userID.String(),
								AlterId: 64,
							}),
						},
					},
				}),
			},
			{
				Tag: "api",
				ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
					PortRange: net.SinglePortRange(cmdPort),
					Listen:    net.NewIPOrDomain(net.LocalHostIP),
				}),
				ProxySettings: serial.ToTypedMessage(&dokodemo.Config{
					Address: net.NewIPOrDomain(dest.Address),
					Port:    uint32(dest.Port),
					NetworkList: &net.NetworkList{
						Network: []net.Network{net.Network_TCP},
					},
				}),
			},
		},
		Outbound: []*core.OutboundHandlerConfig{
			{
				ProxySettings: serial.ToTypedMessage(&freedom.Config{}),
			},
		},
	}

	clientPort := tcp.PickPort()
	clientConfig := &core.Config{
		Inbound: []*core.InboundHandlerConfig{
			{
				ReceiverSettings: serial.ToTypedMessage(&proxyman.ReceiverConfig{
					PortRange: net.SinglePortRange(clientPort),
					Listen:    net.NewIPOrDomain(net.LocalHostIP),
				}),
				ProxySettings: serial.ToTypedMessage(&dokodemo.Config{
					Address: net.NewIPOrDomain(dest.Address),
					Port:    uint32(dest.Port),
					NetworkList: &net.NetworkList{
						Network: []net.Network{net.Network_TCP},
					},
				}),
			},
		},
		Outbound: []*core.OutboundHandlerConfig{
			{
				ProxySettings: serial.ToTypedMessage(&outbound.Config{
					Receiver: []*protocol.ServerEndpoint{
						{
							Address: net.NewIPOrDomain(net.LocalHostIP),
							Port:    uint32(serverPort),
							User: []*protocol.User{
								{
									Account: serial.ToTypedMessage(&vmess.Account{
										Id:      userID.String(),
										AlterId: 64,
										SecuritySettings: &protocol.SecurityConfig{
											Type: protocol.SecurityType_AES128_GCM,
										},
									}),
								},
							},
						},
					},
				}),
			},
		},
	}

	servers, err := InitializeServerConfigs(serverConfig, clientConfig)
	if err != nil {
		t.Fatal("Failed to create all servers", err)
	}
	defer CloseAllServers(servers)

	if err := testTCPConn(clientPort, 10240*1024, time.Second*20)(); err != nil {
		t.Fatal(err)
	}

	cmdConn, err := grpc.Dial(fmt.Sprintf("127.0.0.1:%d", cmdPort), grpc.WithInsecure(), grpc.WithBlock())
	common.Must(err)
	defer cmdConn.Close()

	const name = "user>>>test>>>traffic>>>uplink"
	sClient := statscmd.NewStatsServiceClient(cmdConn)

	sresp, err := sClient.GetStats(context.Background(), &statscmd.GetStatsRequest{
		Name:   name,
		Reset_: true,
	})
	common.Must(err)
	assert(sresp.Stat.Name, Equals, name)
	assert(sresp.Stat.Value, Equals, int64(10240*1024))

	sresp, err = sClient.GetStats(context.Background(), &statscmd.GetStatsRequest{
		Name: name,
	})
	common.Must(err)
	assert(sresp.Stat.Name, Equals, name)
	assert(sresp.Stat.Value, Equals, int64(0))

	sresp, err = sClient.GetStats(context.Background(), &statscmd.GetStatsRequest{
		Name:   "inbound>>>vmess>>>traffic>>>uplink",
		Reset_: true,
	})
	common.Must(err)
	assert(sresp.Stat.Value, GreaterThan, int64(10240*1024))
}
