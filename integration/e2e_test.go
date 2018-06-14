/*
Copyright IBM Corp All Rights Reserved.

SPDX-License-Identifier: Apache-2.0
*/

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/hyperledger/fabric/integration/world"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/gbytes"

	"github.com/tedsuo/ifrit"
)

const EVMSCC = "evmscc"

var _ = Describe("EndToEnd", func() {
	var (
		client     *docker.Client
		w          world.World
		deployment world.Deployment
	)

	BeforeEach(func() {
		var err error

		client, err = docker.NewClientFromEnv()
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		// Stop the docker constainers for zookeeper and kafka
		for _, cont := range w.LocalStoppers {
			cont.Stop()
		}

		// Stop the running chaincode containers
		filters := map[string][]string{}
		filters["name"] = []string{fmt.Sprintf("%s-%s", deployment.Chaincode.Name, deployment.Chaincode.Version)}
		allContainers, _ := client.ListContainers(docker.ListContainersOptions{
			Filters: filters,
		})
		if len(allContainers) > 0 {
			for _, container := range allContainers {
				client.RemoveContainer(docker.RemoveContainerOptions{
					ID:    container.ID,
					Force: true,
				})
			}
		}

		// Remove chaincode image
		filters = map[string][]string{}
		filters["label"] = []string{fmt.Sprintf("org.hyperledger.fabric.chaincode.id.name=%s", deployment.Chaincode.Name)}
		images, _ := client.ListImages(docker.ListImagesOptions{
			Filters: filters,
		})
		if len(images) > 0 {
			for _, image := range images {
				client.RemoveImage(image.ID)
			}
		}

		// Stop the orderers and peers
		for _, localProc := range w.LocalProcess {
			localProc.Signal(syscall.SIGTERM)
			Eventually(localProc.Wait(), 5*time.Second).Should(Receive())
			localProc.Signal(syscall.SIGKILL)
			Eventually(localProc.Wait(), 5*time.Second).Should(Receive())
		}

		// Remove any started networks
		if w.Network != nil {
			client.RemoveNetwork(w.Network.Name)
		}
	})

	It("executes a basic solo network with 2 orgs", func() {
		w = world.GenerateBasicConfig("solo", 1, 2, testDir, components)

		deployment = world.Deployment{
			Channel: "testchannel",
			Chaincode: world.Chaincode{
				Name:     "evmscc",
				Version:  "0.0",
				Path:     "github.com/hyperledger/fabric-chaincode-evm/plugin",
				ExecPath: os.Getenv("PATH"),
			},
			InitArgs: `{"Args":["init"]}`,
			Policy:   `OR ('Org1MSP.member','Org2MSP.member')`,
			Orderer:  "127.0.0.1:7050",
		}

		w.SetupWorld(deployment)

		By("installing a SimpleStorage SmartContract")
		adminPeer := components.Peer()
		adminPeer.LogLevel = "debug"
		adminPeer.ConfigDir = filepath.Join(testDir, "peer0.org1.example.com")
		adminPeer.MSPConfigPath = filepath.Join(testDir, "crypto", "peerOrganizations", "org1.example.com", "users", "Admin@org1.example.com", "msp")

		adminRunner := adminPeer.InvokeChaincode(EVMSCC, deployment.Channel, `{"Args":["0000000000000000000000000000000000000000", "6060604052341561000f57600080fd5b60d38061001d6000396000f3006060604052600436106049576000357c0100000000000000000000000000000000000000000000000000000000900463ffffffff16806360fe47b114604e5780636d4ce63c14606e575b600080fd5b3415605857600080fd5b606c60048080359060200190919050506094565b005b3415607857600080fd5b607e609e565b6040518082815260200191505060405180910390f35b8060008190555050565b600080549050905600a165627a7a72305820122f55f799d70b5f6dbfd4312efb65cdbfaacddedf7c36249b8b1e915a8dd85b0029"]}`, deployment.Orderer)
		execute(adminRunner)
		output := adminRunner.Err().Contents()

		contractAddr := string(regexp.MustCompile(`Chaincode invoke successful. result: status:200 payload:"([0-9a-fA-F]{40})"`).FindSubmatch(output)[1])
		Expect(contractAddr).ToNot(BeEmpty())

		By("invoking the smartcontract")
		args := fmt.Sprintf(`{"Args":["%s", "60fe47b10000000000000000000000000000000000000000000000000000000000000003"]}`, contractAddr)
		adminRunner = adminPeer.InvokeChaincode(EVMSCC, deployment.Channel, args, deployment.Orderer)
		execute(adminRunner)
		Eventually(adminRunner.Err()).Should(gbytes.Say("Chaincode invoke successful. result: status:200"))

		By("querying the chaincode again")
		args = fmt.Sprintf(`{"Args":["%s", "6d4ce63c"]}`, contractAddr)
		adminRunner = adminPeer.QueryChaincode(EVMSCC, deployment.Channel, args)
		adminRunner.Command.Args = append(adminRunner.Command.Args, "--hex")
		execute(adminRunner)
		Eventually(adminRunner.Buffer()).Should(gbytes.Say("0000000000000000000000000000000000000000000000000000000000000003"))
	})
})

func execute(r ifrit.Runner) (err error) {
	p := ifrit.Invoke(r)
	Eventually(p.Ready()).Should(BeClosed())
	Eventually(p.Wait(), 30*time.Second).Should(Receive(&err))
	return err
}