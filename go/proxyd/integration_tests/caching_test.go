package integration_tests

import (
	"bytes"
	"fmt"
	"github.com/alicebob/miniredis"
	"github.com/ethereum-optimism/optimism/go/proxyd"
	"github.com/stretchr/testify/require"
	"os"
	"testing"
	"time"
)

func TestCaching(t *testing.T) {
	redis, err := miniredis.Run()
	require.NoError(t, err)
	defer redis.Close()

	backend := NewMockBackend(RPCResponseHandler(map[string]string{
		"eth_chainId":          "0x420",
		"net_version":          "0x1234",
		"eth_blockNumber":      "0x64",
		"eth_getBlockByNumber": "dummy_block",
	}))
	defer backend.Close()

	require.NoError(t, os.Setenv("GOOD_BACKEND_RPC_URL", backend.URL()))
	require.NoError(t, os.Setenv("REDIS_URL", fmt.Sprintf("redis://127.0.0.1:%s", redis.Port())))
	config := ReadConfig("caching")
	client := NewProxydClient("http://127.0.0.1:8545")
	shutdown, err := proxyd.Start(config)
	require.NoError(t, err)
	defer shutdown()

	// allow time for the block number fetcher to fire
	time.Sleep(1500 * time.Millisecond)

	tests := []struct {
		method   string
		params   []interface{}
		response string
	}{
		{
			"eth_chainId",
			nil,
			"{\"jsonrpc\": \"2.0\", \"result\": \"0x420\", \"id\": 999}",
		},
		{
			"net_version",
			nil,
			"{\"jsonrpc\": \"2.0\", \"result\": \"0x1234\", \"id\": 999}",
		},
		{
			"eth_getBlockByNumber",
			[]interface{}{
				"0x1",
				true,
			},
			"{\"jsonrpc\": \"2.0\", \"result\": \"dummy_block\", \"id\": 999}",
		},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			_, _, err := client.SendRPC(tt.method, tt.params)
			require.NoError(t, err)
			res, _, err := client.SendRPC(tt.method, tt.params)
			require.NoError(t, err)
			RequireEqualJSON(t, []byte(tt.response), res)
			var count int
			for _, req := range backend.Requests() {
				if bytes.Contains(req.Body, []byte(tt.method)) {
					count++
				}
			}
			require.Equal(t, 1, count)
			backend.Reset()
		})
	}
}
