package tunnel

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

var tunnelProtoJSONMarshal = protojson.MarshalOptions{UseProtoNames: true}

func marshalTunnelPayload(value interface{}) (json.RawMessage, error) {
	if message, ok := value.(proto.Message); ok && hasPopulatedOneof(message.ProtoReflect()) {
		return tunnelProtoJSONMarshal.Marshal(message)
	}
	return json.Marshal(value)
}

func hasPopulatedOneof(message protoreflect.Message) bool {
	found := false
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if oneof := field.ContainingOneof(); oneof != nil && !oneof.IsSynthetic() {
			found = true
			return false
		}
		switch {
		case field.IsMap():
			if field.MapValue().Message() == nil {
				break
			}
			value.Map().Range(func(_ protoreflect.MapKey, item protoreflect.Value) bool {
				found = hasPopulatedOneof(item.Message())
				return !found
			})
		case field.IsList() && field.Message() != nil:
			list := value.List()
			for i := 0; i < list.Len(); i++ {
				if hasPopulatedOneof(list.Get(i).Message()) {
					found = true
					return false
				}
			}
		case field.Message() != nil:
			found = hasPopulatedOneof(value.Message())
		}
		return !found
	})
	return found
}

func unmarshalTunnelPayload(data []byte, value interface{}) error {
	message, ok := value.(proto.Message)
	if !ok {
		return json.Unmarshal(data, value)
	}
	if err := protojson.Unmarshal(data, message); err == nil {
		return nil
	} else if legacyErr := json.Unmarshal(data, value); legacyErr != nil {
		return fmt.Errorf("decode protobuf JSON: %v (legacy JSON: %v)", err, legacyErr)
	}
	return nil
}
