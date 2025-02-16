package proxyd

import (
	"context"

	"github.com/go-redis/redis/v8"
	"github.com/golang/snappy"
	lru "github.com/hashicorp/golang-lru"
)

type Cache interface {
	Get(ctx context.Context, key string) (string, error)
	Put(ctx context.Context, key string, value string) error
}

const (
	// assuming an average RPCRes size of 3 KB
	memoryCacheLimit = 4096
)

type cache struct {
	lru *lru.Cache
}

func newMemoryCache() *cache {
	rep, _ := lru.New(memoryCacheLimit)
	return &cache{rep}
}

func (c *cache) Get(ctx context.Context, key string) (string, error) {
	if val, ok := c.lru.Get(key); ok {
		return val.(string), nil
	}
	return "", nil
}

func (c *cache) Put(ctx context.Context, key string, value string) error {
	c.lru.Add(key, value)
	return nil
}

type redisCache struct {
	rdb *redis.Client
}

func newRedisCache(url string) (*redisCache, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	rdb := redis.NewClient(opts)
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		return nil, wrapErr(err, "error connecting to redis")
	}
	return &redisCache{rdb}, nil
}

func (c *redisCache) Get(ctx context.Context, key string) (string, error) {
	val, err := c.rdb.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	} else if err != nil {
		RecordRedisError("CacheGet")
		return "", err
	}
	return val, nil
}

func (c *redisCache) Put(ctx context.Context, key string, value string) error {
	err := c.rdb.Set(ctx, key, value, 0).Err()
	if err != nil {
		RecordRedisError("CacheSet")
	}
	return err
}

type cacheWithCompression struct {
	cache Cache
}

func newCacheWithCompression(cache Cache) *cacheWithCompression {
	return &cacheWithCompression{cache}
}

func (c *cacheWithCompression) Get(ctx context.Context, key string) (string, error) {
	encodedVal, err := c.cache.Get(ctx, key)
	if err != nil {
		return "", err
	}
	if encodedVal == "" {
		return "", nil
	}
	val, err := snappy.Decode(nil, []byte(encodedVal))
	if err != nil {
		return "", err
	}
	return string(val), nil
}

func (c *cacheWithCompression) Put(ctx context.Context, key string, value string) error {
	encodedVal := snappy.Encode(nil, []byte(value))
	return c.cache.Put(ctx, key, string(encodedVal))
}

type GetLatestBlockNumFn func(ctx context.Context) (uint64, error)
type GetLatestGasPriceFn func(ctx context.Context) (uint64, error)

type RPCCache interface {
	GetRPC(ctx context.Context, req *RPCReq) (*RPCRes, error)
	PutRPC(ctx context.Context, req *RPCReq, res *RPCRes) error
}

type rpcCache struct {
	cache    Cache
	handlers map[string]RPCMethodHandler
}

func newRPCCache(cache Cache, getLatestBlockNumFn GetLatestBlockNumFn, getLatestGasPriceFn GetLatestGasPriceFn, numBlockConfirmations int) RPCCache {
	handlers := map[string]RPCMethodHandler{
		"eth_chainId":          &StaticMethodHandler{},
		"net_version":          &StaticMethodHandler{},
		"eth_getBlockByNumber": &EthGetBlockByNumberMethodHandler{cache, getLatestBlockNumFn, numBlockConfirmations},
		"eth_getBlockRange":    &EthGetBlockRangeMethodHandler{cache, getLatestBlockNumFn, numBlockConfirmations},
		"eth_blockNumber":      &EthBlockNumberMethodHandler{getLatestBlockNumFn},
		"eth_gasPrice":         &EthGasPriceMethodHandler{getLatestGasPriceFn},
		"eth_call":             &EthCallMethodHandler{cache, getLatestBlockNumFn, numBlockConfirmations},
	}
	return &rpcCache{
		cache:    cache,
		handlers: handlers,
	}
}

func (c *rpcCache) GetRPC(ctx context.Context, req *RPCReq) (*RPCRes, error) {
	handler := c.handlers[req.Method]
	if handler == nil {
		return nil, nil
	}
	res, err := handler.GetRPCMethod(ctx, req)
	if res != nil {
		if res == nil {
			RecordCacheMiss(req.Method)
		} else {
			RecordCacheHit(req.Method)
		}
	}
	return res, err
}

func (c *rpcCache) PutRPC(ctx context.Context, req *RPCReq, res *RPCRes) error {
	handler := c.handlers[req.Method]
	if handler == nil {
		return nil
	}
	return handler.PutRPCMethod(ctx, req, res)
}
