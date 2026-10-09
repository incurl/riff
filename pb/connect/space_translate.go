package connect

import (
	"github.com/pocketbase/pocketbase/core"

	v1 "github.com/incurl/riff/proto/gen/api/v1"
)

// recordToSpace converts a PocketBase `spaces` record to the
// Memos `Space` proto. Includes the current-user role and
// member count, which the proto marks as "output only".
func recordToSpace(app core.App, rec *core.Record, viewerID string) *v1.Space {
	if rec == nil {
		return nil
	}
	uid := rec.GetString("uid")

	// Icon: the proto uses a oneof (Emoji or Lucide). We
	// only store the emoji form (our schema has a single
	// text field); the Lucide branch is left empty.
	var icon *v1.Space_Icon
	if e := rec.GetString("icon"); e != "" {
		icon = &v1.Space_Icon{
			Value: &v1.Space_Icon_Emoji{Emoji: e},
		}
	}

	sp := &v1.Space{
		Name:        "spaces/" + uid,
		Title:       rec.GetString("title"),
		Description: rec.GetString("description"),
		Icon:        icon,
	}

	// Resolve viewer role.
	if viewerID != "" {
		rows, err := app.FindRecordsByFilter("space_members",
			"user={:uid} && space={:sid} && status='ACTIVE'",
			"", 1, 0,
			map[string]any{"uid": viewerID, "sid": rec.Id},
		)
		if err == nil && len(rows) > 0 {
			sp.CurrentUserRole = roleToProto(rows[0].GetString("role"))
		}
	}

	// Member count (accepted only).
	members, _ := app.FindRecordsByFilter("space_members",
		"space={:sid} && status='ACTIVE'",
		"", 0, 0,
		map[string]any{"sid": rec.Id},
	)
	sp.MemberCount = int32(len(members))
	_ = uid
	return sp
}

// recordToSpaceMember converts a space_members record to a
// Memos SpaceMember proto.
func recordToSpaceMember(app core.App, rec *core.Record) *v1.SpaceMember {
	if rec == nil {
		return nil
	}
	username := lookupUsername(app, rec.GetString("user"))
	spaceUID := lookupSpaceUID(app, rec.GetString("space"))
	return &v1.SpaceMember{
		Name: "spaces/" + spaceUID + "/members/" + username,
		User: "users/" + username,
		Role: roleToProto(rec.GetString("role")),
	}
}

// recordToSpaceInvitation converts a space_members row with
// status=INVITED to a SpaceInvitation proto.
//
// The Space is denormalized onto the invitation so an invitee can see which
// space they are being asked to join without holding the membership that
// GetSpace requires.
func recordToSpaceInvitation(app core.App, rec *core.Record) *v1.SpaceInvitation {
	if rec == nil {
		return nil
	}
	username := lookupUsername(app, rec.GetString("user"))
	spaceUID := lookupSpaceUID(app, rec.GetString("space"))

	invitation := &v1.SpaceInvitation{
		Name:    "spaces/" + spaceUID + "/invitations/" + username,
		Invitee: "users/" + username,
		Role:    roleToProto(rec.GetString("role")),
	}
	if space, err := app.FindRecordById("spaces", rec.GetString("space")); err == nil {
		invitation.Space = recordToSpace(app, space, "")
	}
	return invitation
}

func roleToProto(r string) v1.SpaceMember_Role {
	switch r {
	case "ADMIN":
		return v1.SpaceMember_ADMIN
	case "USER":
		return v1.SpaceMember_USER
	default:
		return v1.SpaceMember_ROLE_UNSPECIFIED
	}
}

func roleFromProto(r v1.SpaceMember_Role) string {
	switch r {
	case v1.SpaceMember_ADMIN:
		return "ADMIN"
	case v1.SpaceMember_USER:
		return "USER"
	default:
		return ""
	}
}
