package synclive

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

type connStateStoreMock struct {
	roomIDToRoom        map[string]*SortableRoom
	userIDToJoinedRooms map[string][]string
	userIDToPosition    map[string]int64
}

func (s *connStateStoreMock) LoadRoom(roomID string) *SortableRoom {
	return s.roomIDToRoom[roomID]
}
func (s *connStateStoreMock) Load(userID string) (joinedRoomIDs []string, initialLoadPosition int64, err error) {
	joinedRoomIDs = s.userIDToJoinedRooms[userID]
	initialLoadPosition = s.userIDToPosition[userID]
	if initialLoadPosition == 0 {
		initialLoadPosition = 1 // so we don't continually load the same rooms
	}
	return
}

// Sync an account with 3 rooms and check that we can grab all rooms and they are sorted correctly initially. Checks
// that basic UPDATE and DELETE/INSERT works when tracking all rooms.
func TestConnStateInitial(t *testing.T) {
	connID := ConnID{
		SessionID: "s",
		DeviceID:  "d",
	}
	userID := "@alice:localhost"
	roomA := "!a:localhost"
	roomB := "!b:localhost"
	roomC := "!c:localhost"
	timestampNow := int64(1632131678061)
	// initial sort order B, C, A
	cs := NewConnState(userID, &connStateStoreMock{
		userIDToJoinedRooms: map[string][]string{
			userID: {roomA, roomB, roomC},
		},
		roomIDToRoom: map[string]*SortableRoom{
			roomA: {
				RoomID:               roomA,
				Name:                 "Room A",
				LastMessageTimestamp: timestampNow - 8000,
			},
			roomB: {
				RoomID:               roomB,
				Name:                 "Room B",
				LastMessageTimestamp: timestampNow,
			},
			roomC: {
				RoomID:               roomC,
				Name:                 "Room C",
				LastMessageTimestamp: timestampNow - 4000,
			},
		},
	})
	if userID != cs.UserID() {
		t.Fatalf("UserID returned wrong value, got %v want %v", cs.UserID(), userID)
	}
	res, err := cs.HandleIncomingRequest(context.Background(), connID, &Request{
		Sort: []string{SortByRecency},
		Rooms: SliceRanges([][2]int64{
			{0, 9},
		}),
	})
	if err != nil {
		t.Fatalf("HandleIncomingRequest returned error : %s", err)
	}
	checkResponse(t, false, res, &Response{
		Count: 3,
		Ops: []ResponseOp{
			&ResponseOpRange{
				Operation: "SYNC",
				Range:     []int64{0, 9},
				Rooms: []Room{
					{
						RoomID: roomB,
						Name:   "Room B",
					},
					{
						RoomID: roomC,
						Name:   "Room C",
					},
					{
						RoomID: roomA,
						Name:   "Room A",
					},
				},
			},
		},
	})

	// bump A to the top
	cs.PushNewEvent(&EventData{
		event:     json.RawMessage(`{}`),
		roomID:    roomA,
		eventType: "unimportant",
		timestamp: timestampNow + 1000,
	})

	// request again for the diff
	res, err = cs.HandleIncomingRequest(context.Background(), connID, &Request{
		Sort: []string{SortByRecency},
		Rooms: SliceRanges([][2]int64{
			{0, 9},
		}),
	})
	if err != nil {
		t.Fatalf("HandleIncomingRequest returned error : %s", err)
	}
	checkResponse(t, true, res, &Response{
		Count: 3,
		Ops: []ResponseOp{
			&ResponseOpSingle{
				Operation: "DELETE",
				Index:     intPtr(2),
			},
			&ResponseOpSingle{
				Operation: "INSERT",
				Index:     intPtr(0),
				Room: &Room{
					RoomID: roomA,
				},
			},
		},
	})

	// another message should just update
	cs.PushNewEvent(&EventData{
		event:     json.RawMessage(`{}`),
		roomID:    roomA,
		eventType: "still unimportant",
		timestamp: timestampNow + 2000,
	})
	res, err = cs.HandleIncomingRequest(context.Background(), connID, &Request{
		Sort: []string{SortByRecency},
		Rooms: SliceRanges([][2]int64{
			{0, 9},
		}),
	})
	if err != nil {
		t.Fatalf("HandleIncomingRequest returned error : %s", err)
	}
	checkResponse(t, true, res, &Response{
		Count: 3,
		Ops: []ResponseOp{
			&ResponseOpSingle{
				Operation: "UPDATE",
				Index:     intPtr(0),
				Room: &Room{
					RoomID: roomA,
				},
			},
		},
	})
}

func checkResponse(t *testing.T, checkRoomIDsOnly bool, got, want *Response) {
	t.Helper()
	if want.Count > 0 {
		if got.Count != want.Count {
			t.Errorf("response Count: got %d want %d", got.Count, want.Count)
		}
	}
	if len(want.Ops) > 0 {
		t.Logf("got %v", serialise(t, got))
		t.Logf("want %v", serialise(t, want))
		defer func() {
			t.Helper()
			if !t.Failed() {
				t.Logf("OK!")
			}
		}()
		if len(got.Ops) != len(want.Ops) {
			t.Fatalf("got %d ops, want %d", len(got.Ops), len(want.Ops))
		}
		for i, wantOpVal := range want.Ops {
			gotOp := got.Ops[i]
			if gotOp.Op() != wantOpVal.Op() {
				t.Errorf("operation i=%d got '%s' want '%s'", i, gotOp.Op(), wantOpVal.Op())
			}
			switch wantOp := wantOpVal.(type) {
			case *ResponseOpRange:
				gotOpRange, ok := gotOp.(*ResponseOpRange)
				if !ok {
					t.Fatalf("operation i=%d (%s) want type ResponseOpRange but it isn't", i, gotOp.Op())
				}
				if !reflect.DeepEqual(gotOpRange.Range, wantOp.Range) {
					t.Errorf("operation i=%d (%s) got range %v want range %v", i, gotOp.Op(), gotOpRange.Range, wantOp.Range)
				}
				if len(gotOpRange.Rooms) != len(wantOp.Rooms) {
					t.Fatalf("operation i=%d (%s) got %d rooms in array, want %d", i, gotOp.Op(), len(gotOpRange.Rooms), len(wantOp.Rooms))
				}
				for j := range wantOp.Rooms {
					checkRoomsEqual(t, checkRoomIDsOnly, &gotOpRange.Rooms[j], &wantOp.Rooms[j])
				}
			case *ResponseOpSingle:
				gotOpSingle, ok := gotOp.(*ResponseOpSingle)
				if !ok {
					t.Fatalf("operation i=%d (%s) want type ResponseOpSingle but it isn't", i, gotOp.Op())
				}
				if *gotOpSingle.Index != *wantOp.Index {
					t.Errorf("operation i=%d (%s) single op on index %d want index %d", i, gotOp.Op(), *gotOpSingle.Index, *wantOp.Index)
				}
				checkRoomsEqual(t, checkRoomIDsOnly, gotOpSingle.Room, wantOp.Room)
			}
		}
	}
}

func checkRoomsEqual(t *testing.T, checkRoomIDsOnly bool, got, want *Room) {
	t.Helper()
	if got == nil && want == nil {
		return // e.g DELETE ops
	}
	if (got == nil && want != nil) || (want == nil && got != nil) {
		t.Fatalf("nil room, got %+v want %+v", got, want)
	}
	if checkRoomIDsOnly {
		if got.RoomID != want.RoomID {
			t.Fatalf("got room '%s' want room '%s'", got.RoomID, want.RoomID)
		}
		return
	}
	gotBytes, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("cannot marshal got room: %s", err)
	}
	wantBytes, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("cannot marshal want room: %s", err)
	}
	if !bytes.Equal(gotBytes, wantBytes) {
		t.Errorf("rooms do not match,\ngot  %s want %s", string(gotBytes), string(wantBytes))
	}
}

func serialise(t *testing.T, thing interface{}) string {
	b, err := json.Marshal(thing)
	if err != nil {
		t.Fatalf("cannot serialise: %s", err)
	}
	return string(b)
}

func intPtr(val int) *int {
	return &val
}