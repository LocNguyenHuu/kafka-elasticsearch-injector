package kafka

import (
	"context"
	"errors"
	"reflect"
	"time"

	"encoding/json"

	"sync"

	"github.com/Shopify/sarama"
	"github.com/linkedin/goavro/v2"
	"github.com/inloco/kafka-elasticsearch-injector/src/models"
	e "github.com/inloco/kafka-elasticsearch-injector/src/errors"
	"github.com/inloco/kafka-elasticsearch-injector/src/schema_registry"
)

// DecodeMessageFunc extracts a user-domain request object from an Kafka
// message object. It's designed to be used in Kafka consumers.
// One straightforward DecodeMessageFunc could be something that
// Avro decodes the message body to the concrete response type.
type DecodeMessageFunc func(context.Context, *sarama.ConsumerMessage) (record *models.Record, err error)

const kafkaTimestampKey = "@timestamp"

type Decoder struct {
	SchemaRegistry *schema_registry.SchemaRegistry
	CodecCache     sync.Map
}

func (d *Decoder) DeserializerFor(recordType string) DecodeMessageFunc {
	if recordType == "json" {
		return d.JsonMessageToRecord
	} else {
		return d.AvroMessageToRecord
	}
}

func (d *Decoder) AvroMessageToRecord(context context.Context, msg *sarama.ConsumerMessage) (*models.Record, error) {
	if msg.Value == nil {
		return nil, new(e.ErrNilMessage)
	}

	schemaId := getSchemaId(msg)
	avroRecord := msg.Value[5:]
	schema, err := d.SchemaRegistry.GetSchema(schemaId)
	if err != nil {
		return nil, err
	}
	var codec *goavro.Codec
	if codecI, ok := d.CodecCache.Load(schemaId); ok {
		codec, ok = codecI.(*goavro.Codec)
	}

	if codec == nil {
		codec, err = goavro.NewCodec(schema)
		if err != nil {
			return nil, err
		}

		d.CodecCache.Store(schemaId, codec)
	}

	native, _, err := codec.NativeFromBinary(avroRecord)
	if err != nil {
		return nil, err
	}

	parsedNative := make(map[string]interface{})
	nativeType := reflect.ValueOf(native)
	if nativeType.Kind() != reflect.Map {
		return nil, errors.New("could not unmarshall record JSON into map")
	}
	for _, key := range nativeType.MapKeys() {
		if key.Kind() != reflect.String {
			return nil, errors.New("could not unmarshall record JSON into map keyed by string")
		}
		parsedNative[key.String()] = nativeType.MapIndex(key).Interface()
	}

	parsedNative[kafkaTimestampKey] = makeTimestamp(msg.Timestamp)

	return &models.Record{
		Topic:     msg.Topic,
		Partition: msg.Partition,
		Offset:    msg.Offset,
		Timestamp: msg.Timestamp,
		Json:      parsedNative,
	}, nil
}

func makeTimestamp(timestamp time.Time) int64 {
	return timestamp.UnixNano() / int64(time.Millisecond)
}

func (d *Decoder) JsonMessageToRecord(context context.Context, msg *sarama.ConsumerMessage) (*models.Record, error) {
	var jsonValue map[string]interface{}
	err := json.Unmarshal(msg.Value, &jsonValue)

	if err != nil {
		return nil, err
	}

	jsonValue[kafkaTimestampKey] = makeTimestamp(msg.Timestamp)

	return &models.Record{
		Topic:     msg.Topic,
		Partition: msg.Partition,
		Offset:    msg.Offset,
		Timestamp: msg.Timestamp,
		Json:      jsonValue,
	}, nil
}

func getSchemaId(msg *sarama.ConsumerMessage) int32 {
	schemaIdBytes := msg.Value[1:5]
	return int32(schemaIdBytes[0])<<24 | int32(schemaIdBytes[1])<<16 | int32(schemaIdBytes[2])<<8 | int32(schemaIdBytes[3])
}
