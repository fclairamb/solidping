package checkkafka

import (
	"crypto/sha256"
	"crypto/sha512"

	"github.com/xdg-go/scram"
)

// scramClient implements the sarama.SCRAMClient interface.
type scramClient struct {
	*scram.ClientConversation
	hashGen scram.HashGeneratorFcn
}

// Begin starts a new SCRAM conversation.
func (c *scramClient) Begin(userName, password, authzID string) error {
	client, err := c.hashGen.NewClient(userName, password, authzID)
	if err != nil {
		return err
	}

	c.ClientConversation = client.NewConversation()

	return nil
}

// Step processes a server challenge and returns a response.
func (c *scramClient) Step(challenge string) (string, error) {
	return c.ClientConversation.Step(challenge)
}

// Done returns whether the conversation is complete.
func (c *scramClient) Done() bool {
	return c.ClientConversation.Done()
}

func newSHA256Generator() scram.HashGeneratorFcn {
	return sha256.New
}

func newSHA512Generator() scram.HashGeneratorFcn {
	return sha512.New
}
