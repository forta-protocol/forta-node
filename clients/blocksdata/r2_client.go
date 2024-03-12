package blocksdata

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/forta-network/forta-core-go/protocol"
	"github.com/forta-network/forta-core-go/utils/httpclient"
	"google.golang.org/protobuf/proto"
)

type blocksDataClient struct {
	dispatcherURL  *url.URL
	dispatcherPath string
}

func NewCombinedBlockEventsClient(dispatcherURL string) *blocksDataClient {
	u, _ := url.Parse(dispatcherURL)

	return &blocksDataClient{
		dispatcherURL:  u,
		dispatcherPath: u.Path,
	}
}

type PresignedURLItem struct {
	Bucket       int64  `json:"bucket"`
	PresignedURL string `json:"presignedURL"`
	ExpiresAt    int64  `json:"expiresAt"`
}

func (c *blocksDataClient) GetBlocksData(bucket int64) (_ *protocol.BlocksData, err error) {
	c.dispatcherURL.Path, err = url.JoinPath(c.dispatcherPath, fmt.Sprintf("%d", bucket))
	if err != nil {
		return nil, err
	}

	resp, err := httpclient.Default.Get(c.dispatcherURL.String())
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	var item PresignedURLItem
	err = json.NewDecoder(resp.Body).Decode(&item)
	if err != nil {
		return nil, err
	}

	if item.ExpiresAt < time.Now().Unix() {
		return nil, fmt.Errorf("presigned URL expired")
	}

	resp, err = httpclient.Default.Get(item.PresignedURL)
	if err != nil {
		return nil, err
	}

	b, err := io.ReadAll(brotli.NewReader(resp.Body))
	if err != nil {
		return nil, err
	}

	var blocks protocol.BlocksData

	err = proto.Unmarshal(b, &blocks)
	if err != nil {
		return nil, err
	}

	return &blocks, nil
}