package syncv3

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/matrix-org/sync-v3/sync2"
	"github.com/matrix-org/sync-v3/sync3"
	"github.com/matrix-org/sync-v3/testutils"
)

// Test that multiple lists can be independently scrolled through
func TestMultipleLists(t *testing.T) {
	boolTrue := true
	boolFalse := false
	pqString := testutils.PrepareDBConnectionString()
	// setup code
	v2 := runTestV2Server(t)
	v3 := runTestServer(t, v2, pqString)
	defer v2.close()
	defer v3.close()
	alice := "@TestMultipleLists_alice:localhost"
	aliceToken := "ALICE_BEARER_TOKEN_TestMultipleLists"
	var allRooms []roomEvents
	var encryptedRooms []roomEvents
	var unencryptedRooms []roomEvents
	baseTimestamp := time.Now()
	// make 10 encrypted rooms and make 10 unencrypted rooms. Room 0 is most recent to ease checks
	for i := 0; i < 10; i++ {
		ts := baseTimestamp.Add(time.Duration(-1*i) * time.Second)
		encRoom := roomEvents{
			roomID: fmt.Sprintf("!encrypted_%d:localhost", i),
			events: append(createRoomState(t, alice, ts), []json.RawMessage{
				testutils.NewStateEvent(
					t, "m.room.encryption", "", alice, map[string]interface{}{
						"algorithm":            "m.megolm.v1.aes-sha2",
						"rotation_period_ms":   604800000,
						"rotation_period_msgs": 100,
					}, testutils.WithTimestamp(ts),
				),
			}...),
		}
		room := roomEvents{
			roomID: fmt.Sprintf("!unencrypted_%d:localhost", i),
			events: createRoomState(t, alice, ts),
		}
		allRooms = append(allRooms, []roomEvents{encRoom, room}...)
		encryptedRooms = append(encryptedRooms, encRoom)
		unencryptedRooms = append(unencryptedRooms, room)
	}
	v2.addAccount(alice, aliceToken)
	v2.queueResponse(alice, sync2.SyncResponse{
		Rooms: sync2.SyncRoomsResponse{
			Join: v2JoinTimeline(allRooms...),
		},
	})

	// request 2 lists, one set encrypted, one set unencrypted
	res := v3.mustDoV3Request(t, aliceToken, sync3.Request{
		Lists: []sync3.RequestList{
			{
				Sort: []string{sync3.SortByRecency},
				Rooms: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms
				},
				TimelineLimit: 1,
				Filters: &sync3.RequestFilters{
					IsEncrypted: &boolTrue,
				},
			},
			{
				Sort: []string{sync3.SortByRecency},
				Rooms: sync3.SliceRanges{
					[2]int64{0, 2}, // first 3 rooms
				},
				TimelineLimit: 1,
				Filters: &sync3.RequestFilters{
					IsEncrypted: &boolFalse,
				},
			},
		},
	})

	checkRoomList := func(op *sync3.ResponseOpRange, wantRooms []roomEvents) error {
		if len(op.Rooms) != len(wantRooms) {
			return fmt.Errorf("want %d rooms, got %d", len(wantRooms), len(op.Rooms))
		}
		for i := range wantRooms {
			err := wantRooms[i].MatchRoom(
				op.Rooms[i],
				MatchRoomTimelineMostRecent(1, wantRooms[i].events),
			)
			if err != nil {
				return err
			}
		}
		return nil
	}
	seen := map[int]bool{}
	opMatch := func(op *sync3.ResponseOpRange) error {
		seen[op.List] = true
		if op.List == 0 { // first 3 encrypted rooms
			return checkRoomList(op, encryptedRooms[:3])
		} else if op.List == 1 { // first 3 unencrypted rooms
			return checkRoomList(op, unencryptedRooms[:3])
		}
		return fmt.Errorf("unknown List: %d", op.List)
	}

	MatchResponse(t, res, MatchV3Counts([]int{len(encryptedRooms), len(unencryptedRooms)}), MatchV3Ops(
		MatchV3SyncOp(opMatch), MatchV3SyncOp(opMatch),
	))

	if !seen[0] || !seen[1] {
		t.Fatalf("didn't see both list 0 and 1: %+v", res)
	}
}
