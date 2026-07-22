package main

import (
	"context"
	"log"
	"os"

	"github.com/go-redis/redis/v8"
)

var (
	Rdb *redis.Client
	ctx = context.Background()
)

// InitRedis establishes connection to Redis and starts the Pub/Sub listener.
func InitRedis() {
	redisUrl := os.Getenv("REDIS_URL")
	if redisUrl == "" {
		redisUrl = "localhost:6379"
	}

	Rdb = redis.NewClient(&redis.Options{
		Addr:     redisUrl,
		Password: "", // no password set
		DB:       0,  // use default DB
	})

	_, err := Rdb.Ping(ctx).Result()
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	log.Println("Connected to Redis successfully.")

	// Start background listener for cache invalidation
	go listenForCacheInvalidation()
}

func listenForCacheInvalidation() {
	pubsub := Rdb.Subscribe(ctx, "cognigate:cache:invalidate")
	defer pubsub.Close()

	log.Println("Listening for Redis cache invalidation events on 'cognigate:cache:invalidate'...")
	ch := pubsub.Channel()

	for msg := range ch {
		log.Printf("Received invalidation event for tenant key: %s\n", msg.Payload)
		// In a real implementation, we would invalidate the local memory cache here.
		// For example: memoryCache.Delete(msg.Payload)
	}
}
