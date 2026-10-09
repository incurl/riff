package connect

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"connectrpc.com/connect/v2"
	"github.com/pocketbase/pocketbase/core"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/incurl/riff/pb/hooks"
	v1 "github.com/incurl/riff/proto/gen/api/v1"
	"github.com/incurl/riff/proto/gen/api/v1/apiv1connect"
)

// SpaceServer implements the Memos SpaceService. Phase 4
// wires the methods the Spaces UI needs:
//
//	CRUD:    GetSpace, ListSpaces, CreateSpace, UpdateSpace, DeleteSpace
//	Members: ListSpaceMembers
//	Invites: CreateSpaceInvitation, ListSpaceInvitations,
//	         ListUserSpaceInvitations,
//	         AcceptSpaceInvitation, DeclineSpaceInvitation
//
// Authorization follows the Memos 0.31 design doc: only
// ACTIVE members of a space can read it (non-members get
// 404, not 403), only ADMINs can invite, only the invitee
// can accept/decline, and the last-ADMIN invariant is
// preserved via the existing pb/hooks/space_members.go
// hooks.
type SpaceServer struct {
	apiv1connect.UnimplementedSpaceServiceHandler
	app core.App
}

func NewSpaceServer(app core.App) *SpaceServer { return &SpaceServer{app: app} }

// uidRe matches the Memos UID format we accept for spaces.
var uidRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,34}[a-z0-9]$`)

// GetSpace returns a single space by uid.
func (s *SpaceServer) GetSpace(ctx context.Context, req *v1.GetSpaceRequest) (*v1.Space, error) {
	uid := spaceUIDFromName(req.GetName())
	if uid == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "name is required")
	}
	rec, err := findSpaceByUID(s.app, uid)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "space not found")
	}
	viewerID := currentUserID(ctx)
	// Per design doc: non-members get NotFound.
	if !hooks.IsActiveMember(s.app, viewerID, rec.Id) {
		return nil, connect.NewError(connect.CodeNotFound, "space not found")
	}
	return recordToSpace(s.app, rec, viewerID), nil
}

// ListSpaces returns spaces where the caller is an active
// member.
func (s *SpaceServer) ListSpaces(ctx context.Context, req *v1.ListSpacesRequest) (*v1.ListSpacesResponse, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	limit, offset := paginationFromRequest(0, int(req.GetPageSize()))

	memberships, err := s.app.FindRecordsByFilter("space_members",
		"user={:uid} && status='ACTIVE'",
		"", 0, 0,
		map[string]any{"uid": viewerID},
	)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	if len(memberships) == 0 {
		return &v1.ListSpacesResponse{Spaces: nil, NextPageToken: ""}, nil
	}
	spaceIDs := make([]string, 0, len(memberships))
	for _, m := range memberships {
		spaceIDs = append(spaceIDs, m.GetString("space"))
	}
	orClauses := make([]string, 0, len(spaceIDs))
	params := map[string]any{}
	for i, id := range spaceIDs {
		key := "s" + intToStr(i)
		orClauses = append(orClauses, "id={:"+key+"}")
		params[key] = id
	}
	filter := strings.Join(orClauses, " || ")

	rows, err := s.app.FindRecordsByFilter("spaces", filter, "created", limit, offset, params)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	out := make([]*v1.Space, 0, len(rows))
	for _, r := range rows {
		out = append(out, recordToSpace(s.app, r, viewerID))
	}
	return &v1.ListSpacesResponse{Spaces: out, NextPageToken: ""}, nil
}

// CreateSpace creates a new space. The OnRecordCreateRequest
// hook on the spaces collection auto-sets the owner to the
// caller and inserts an ACTIVE ADMIN space_members row.
func (s *SpaceServer) CreateSpace(ctx context.Context, req *v1.CreateSpaceRequest) (*v1.Space, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	if req.Space == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "space is required")
	}
	uid := req.GetSpaceId()
	if uid == "" {
		uid = slugify(req.Space.GetTitle())
		if uid == "" {
			return nil, connect.NewError(connect.CodeInvalidArgument, "uid or title required")
		}
	}
	if !uidRe.MatchString(uid) {
		return nil, connect.NewError(connect.CodeInvalidArgument, "uid must match ^[a-z0-9][a-z0-9-]{0,34}[a-z0-9]$")
	}

	spaces, err := s.app.FindCollectionByNameOrId("spaces")
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	rec := core.NewRecord(spaces)
	rec.Set("uid", uid)
	rec.Set("title", req.Space.GetTitle())
	rec.Set("description", req.Space.GetDescription())
	if req.Space.Icon != nil {
		if emoji, ok := req.Space.Icon.Value.(*v1.Space_Icon_Emoji); ok {
			rec.Set("icon", emoji.Emoji)
		}
	}
	if err := createRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	return recordToSpace(s.app, rec, viewerID), nil
}

// UpdateSpace updates a space's title/description/icon. Only
// an active ADMIN can do this.
func (s *SpaceServer) UpdateSpace(ctx context.Context, req *v1.UpdateSpaceRequest) (*v1.Space, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	if req.Space == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "space is required")
	}
	uid := spaceUIDFromName(req.Space.GetName())
	rec, err := findSpaceByUID(s.app, uid)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "space not found")
	}
	if !hooks.IsActiveAdmin(s.app, viewerID, rec.Id) {
		return nil, connect.NewError(connect.CodePermissionDenied, "only space admins can update a space")
	}
	if req.Space.Title != "" {
		rec.Set("title", req.Space.Title)
	}
	if req.Space.Description != "" {
		rec.Set("description", req.Space.Description)
	}
	if req.Space.Icon != nil {
		if emoji, ok := req.Space.Icon.Value.(*v1.Space_Icon_Emoji); ok {
			rec.Set("icon", emoji.Emoji)
		}
	}
	if err := updateRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return recordToSpace(s.app, rec, viewerID), nil
}

// DeleteSpace removes a space. The OnRecordDeleteRequest hook cascades the
// deletion to all assigned memos, memberships, invitations and their owned
// resources. Caller must be an active ADMIN.
func (s *SpaceServer) DeleteSpace(ctx context.Context, req *v1.DeleteSpaceRequest) (*emptypb.Empty, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	uid := spaceUIDFromName(req.GetName())
	rec, err := findSpaceByUID(s.app, uid)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "space not found")
	}
	if !hooks.IsActiveAdmin(s.app, viewerID, rec.Id) {
		return nil, connect.NewError(connect.CodePermissionDenied, "only space admins can delete a space")
	}
	if err := deleteRecordRequest(ctx, s.app, rec); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return &emptypb.Empty{}, nil
}

// ListSpaceMembers returns the members of a space.
func (s *SpaceServer) ListSpaceMembers(ctx context.Context, req *v1.ListSpaceMembersRequest) (*v1.ListSpaceMembersResponse, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	uid := spaceUIDFromName(req.GetParent())
	rec, err := findSpaceByUID(s.app, uid)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "space not found")
	}
	if !hooks.IsActiveMember(s.app, viewerID, rec.Id) {
		return nil, connect.NewError(connect.CodeNotFound, "space not found")
	}
	limit, offset := paginationFromRequest(0, int(req.GetPageSize()))
	rows, err := s.app.FindRecordsByFilter("space_members",
		"space={:sid} && status='ACTIVE'",
		"", limit, offset,
		map[string]any{"sid": rec.Id},
	)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	out := make([]*v1.SpaceMember, 0, len(rows))
	for _, r := range rows {
		out = append(out, recordToSpaceMember(s.app, r))
	}
	return &v1.ListSpaceMembersResponse{SpaceMembers: out, NextPageToken: ""}, nil
}

// CreateSpaceInvitation invites a user to a space with the
// given role. Only active ADMINs can invite.
func (s *SpaceServer) CreateSpaceInvitation(ctx context.Context, req *v1.CreateSpaceInvitationRequest) (*v1.SpaceInvitation, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	if req.SpaceInvitation == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "invitation is required")
	}
	uid := spaceUIDFromName(req.GetParent())
	rec, err := findSpaceByUID(s.app, uid)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "space not found")
	}
	if !hooks.IsActiveAdmin(s.app, viewerID, rec.Id) {
		return nil, connect.NewError(connect.CodePermissionDenied, "only space admins can invite")
	}
	inviteeUsername := creatorUsername(req.SpaceInvitation.GetInvitee())
	invitee, err := lookupUserByName(s.app, inviteeUsername)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "invitee not found")
	}
	role := roleFromProto(req.SpaceInvitation.GetRole())
	if role == "" {
		role = "USER"
	}

	// Idempotency: if a row already exists, update it to
	// INVITED with the new role; else create.
	smCol, _ := s.app.FindCollectionByNameOrId("space_members")
	existing, _ := s.app.FindRecordsByFilter("space_members",
		"space={:sid} && user={:uid}",
		"", 1, 0,
		map[string]any{"sid": rec.Id, "uid": invitee.Id},
	)
	var member *core.Record
	if len(existing) > 0 {
		member = existing[0]
		member.Set("status", "INVITED")
		member.Set("role", role)
	} else {
		member = core.NewRecord(smCol)
		member.Set("space", rec.Id)
		member.Set("user", invitee.Id)
		member.Set("status", "INVITED")
		member.Set("role", role)
	}
	// This write deliberately bypasses the createRecordRequest bridge. The
	// space_members BeforeCreate rule only accepts a row the caller owns
	// (the accept path); an admin inviting somebody else must be able to
	// insert an INVITED row for a different user. The /api/po33/spaces/:id/
	// invite REST route is the same escape hatch, and both are admin-gated
	// above.
	if err := s.app.Save(member); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return recordToSpaceInvitation(s.app, member), nil
}

// ListSpaceInvitations lists pending invitations for a space.
// Only active ADMINs of the space can see them.
func (s *SpaceServer) ListSpaceInvitations(ctx context.Context, req *v1.ListSpaceInvitationsRequest) (*v1.ListSpaceInvitationsResponse, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	uid := spaceUIDFromName(req.GetParent())
	rec, err := findSpaceByUID(s.app, uid)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "space not found")
	}
	if !hooks.IsActiveAdmin(s.app, viewerID, rec.Id) {
		return nil, connect.NewError(connect.CodePermissionDenied, "only space admins can list invitations")
	}
	limit, offset := paginationFromRequest(0, int(req.GetPageSize()))
	rows, err := s.app.FindRecordsByFilter("space_members",
		"space={:sid} && status='INVITED'",
		"", limit, offset,
		map[string]any{"sid": rec.Id},
	)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	out := make([]*v1.SpaceInvitation, 0, len(rows))
	for _, r := range rows {
		out = append(out, recordToSpaceInvitation(s.app, r))
	}
	return &v1.ListSpaceInvitationsResponse{SpaceInvitations: out, NextPageToken: ""}, nil
}

// ListUserSpaceInvitations returns invitations addressed to
// the authenticated user.
func (s *SpaceServer) ListUserSpaceInvitations(ctx context.Context, req *v1.ListUserSpaceInvitationsRequest) (*v1.ListUserSpaceInvitationsResponse, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	limit, offset := paginationFromRequest(0, int(req.GetPageSize()))
	rows, err := s.app.FindRecordsByFilter("space_members",
		"user={:uid} && status='INVITED'",
		"", limit, offset,
		map[string]any{"uid": viewerID},
	)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	out := make([]*v1.SpaceInvitation, 0, len(rows))
	for _, r := range rows {
		out = append(out, recordToSpaceInvitation(s.app, r))
	}
	return &v1.ListUserSpaceInvitationsResponse{SpaceInvitations: out, NextPageToken: ""}, nil
}

// AcceptSpaceInvitation flips an INVITED row to ACTIVE. Only the invitee (the
// user the row points to) can accept. The update hook re-checks that the
// caller owns the row and that the transition is INVITED -> ACTIVE.
func (s *SpaceServer) AcceptSpaceInvitation(ctx context.Context, req *v1.AcceptSpaceInvitationRequest) (*v1.SpaceMember, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	member, err := findInvitationByName(s.app, req.GetName(), viewerID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "invitation not found")
	}
	member.Set("status", "ACTIVE")
	if err := updateRecordRequest(ctx, s.app, member); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return recordToSpaceMember(s.app, member), nil
}

// DeclineSpaceInvitation removes an INVITED row. Only the invitee can decline.
func (s *SpaceServer) DeclineSpaceInvitation(ctx context.Context, req *v1.DeclineSpaceInvitationRequest) (*emptypb.Empty, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	member, err := findInvitationByName(s.app, req.GetName(), viewerID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "invitation not found")
	}
	if err := deleteRecordRequest(ctx, s.app, member); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return &emptypb.Empty{}, nil
}

// GetSpaceMember returns one membership by its
// spaces/{space}/members/{username} name. Only active members of the space may
// read it; non-members get NotFound so the space's existence stays hidden.
func (s *SpaceServer) GetSpaceMember(ctx context.Context, req *v1.GetSpaceMemberRequest) (*v1.SpaceMember, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	member, err := findSpaceMemberByName(s.app, req.GetName())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "space member not found")
	}
	if !hooks.IsActiveMember(s.app, viewerID, member.GetString("space")) {
		return nil, connect.NewError(connect.CodeNotFound, "space member not found")
	}
	return recordToSpaceMember(s.app, member), nil
}

// UpdateSpaceMember changes a member's role. The proto reserves update_mask for
// future fields, but only `role` is supported today, so any other path is
// rejected rather than silently ignored.
//
// Authorization and the last-ADMIN invariant both live in the
// OnRecordUpdateRequest hook for space_members, so routing through
// updateRecordRequest applies them for us.
func (s *SpaceServer) UpdateSpaceMember(ctx context.Context, req *v1.UpdateSpaceMemberRequest) (*v1.SpaceMember, error) {
	if currentUserID(ctx) == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	if req.SpaceMember == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, "space_member is required")
	}
	if paths, all := fieldMaskPaths(req.GetUpdateMask()); !all {
		for path := range paths {
			if path != "role" {
				return nil, connect.NewError(connect.CodeInvalidArgument, "only the role field can be updated")
			}
		}
	}

	member, err := findSpaceMemberByName(s.app, req.SpaceMember.GetName())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "space member not found")
	}
	role := roleFromProto(req.SpaceMember.GetRole())
	if role == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "role is required")
	}
	member.Set("role", role)
	if err := updateRecordRequest(ctx, s.app, member); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, err.Error())
	}
	return recordToSpaceMember(s.app, member), nil
}

// DeleteSpaceMember removes a membership. The OnRecordDeleteRequest hook for
// space_members enforces that the caller is the row's own user or an active
// ADMIN, and refuses to drop the last active ADMIN.
func (s *SpaceServer) DeleteSpaceMember(ctx context.Context, req *v1.DeleteSpaceMemberRequest) (*emptypb.Empty, error) {
	if currentUserID(ctx) == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	member, err := findSpaceMemberByName(s.app, req.GetName())
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "space member not found")
	}
	if err := deleteRecordRequest(ctx, s.app, member); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, err.Error())
	}
	return &emptypb.Empty{}, nil
}

// GetSpaceInvitation returns a pending invitation. Both the invitee (who must
// render the prompt without being a member yet) and an active ADMIN of the
// space (who may revoke it) can read it.
func (s *SpaceServer) GetSpaceInvitation(ctx context.Context, req *v1.GetSpaceInvitationRequest) (*v1.SpaceInvitation, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	invitation, err := findInvitationByNameForViewer(s.app, req.GetName(), viewerID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "invitation not found")
	}
	return recordToSpaceInvitation(s.app, invitation), nil
}

// DeleteSpaceInvitation revokes a pending invitation. An active ADMIN does
// this; the invitee declines through DeclineSpaceInvitation instead.
func (s *SpaceServer) DeleteSpaceInvitation(ctx context.Context, req *v1.DeleteSpaceInvitationRequest) (*emptypb.Empty, error) {
	viewerID := currentUserID(ctx)
	if viewerID == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, "authentication required")
	}
	invitation, err := findInvitationByNameForViewer(s.app, req.GetName(), viewerID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, "invitation not found")
	}
	if err := deleteRecordRequest(ctx, s.app, invitation); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err.Error())
	}
	return &emptypb.Empty{}, nil
}

// findSpaceMemberByName resolves a spaces/{space}/members/{username} name to
// the underlying space_members row.
func findSpaceMemberByName(app core.App, name string) (*core.Record, error) {
	space, memberUsername, err := parseSpaceScopedName(app, name, "members")
	if err != nil {
		return nil, err
	}
	user, err := lookupUserByName(app, memberUsername)
	if err != nil {
		return nil, err
	}
	rows, err := app.FindRecordsByFilter("space_members",
		"space={:sid} && user={:uid}",
		"", 1, 0,
		map[string]any{"sid": space.Id, "uid": user.Id},
	)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("space member not found")
	}
	return rows[0], nil
}

// findInvitationByNameForViewer resolves an invitation name and allows the
// callers that may act on it: the invitee themselves, or an active ADMIN of the
// space (who needs it to revoke). Everyone else — including a plain member —
// gets an error, which the handlers surface as NotFound.
func findInvitationByNameForViewer(app core.App, name, viewerID string) (*core.Record, error) {
	space, inviteeUsername, err := parseSpaceScopedName(app, name, "invitations")
	if err != nil {
		return nil, err
	}
	invitee, err := lookupUserByName(app, inviteeUsername)
	if err != nil {
		return nil, err
	}
	if invitee.Id != viewerID && !hooks.IsActiveAdmin(app, viewerID, space.Id) {
		return nil, errors.New("not your invitation")
	}
	rows, err := app.FindRecordsByFilter("space_members",
		"space={:sid} && user={:uid} && status='INVITED'",
		"", 1, 0,
		map[string]any{"sid": space.Id, "uid": invitee.Id},
	)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("invitation not found")
	}
	return rows[0], nil
}

// parseSpaceScopedName splits spaces/{uid}/{kind}/{username} and resolves the
// space. `kind` is "members" or "invitations"; both hang off the same
// space_members row.
func parseSpaceScopedName(app core.App, name, kind string) (*core.Record, string, error) {
	parts := strings.Split(name, "/")
	if len(parts) != 4 || parts[0] != "spaces" || parts[2] != kind {
		return nil, "", errors.New("malformed resource name")
	}
	if parts[1] == "" || parts[3] == "" {
		return nil, "", errors.New("malformed resource name")
	}
	space, err := findSpaceByUID(app, parts[1])
	if err != nil {
		return nil, "", err
	}
	return space, parts[3], nil
}

// findSpaceByUID resolves a space by its uid slug.
func findSpaceByUID(app core.App, uid string) (*core.Record, error) {
	if uid == "" {
		return nil, errors.New("empty uid")
	}
	rows, err := app.FindRecordsByFilter("spaces", "uid={:u}", "", 1, 0, map[string]any{"u": uid})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("space not found")
	}
	return rows[0], nil
}

// findInvitationByName resolves a "spaces/{space}/invitations/{username}"
// resource name to the underlying space_members row. The
// caller must be the invitee (the row's user).
func findInvitationByName(app core.App, name, callerID string) (*core.Record, error) {
	parts := strings.Split(name, "/")
	if len(parts) != 4 || parts[0] != "spaces" || parts[2] != "invitations" {
		return nil, errors.New("malformed invitation name")
	}
	uid, username := parts[1], parts[3]
	space, err := findSpaceByUID(app, uid)
	if err != nil {
		return nil, err
	}
	invitee, err := lookupUserByName(app, username)
	if err != nil {
		return nil, err
	}
	if invitee.Id != callerID {
		return nil, errors.New("not your invitation")
	}
	rows, err := app.FindRecordsByFilter("space_members",
		"space={:sid} && user={:uid} && status='INVITED'",
		"", 1, 0,
		map[string]any{"sid": space.Id, "uid": invitee.Id},
	)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, errors.New("invitation not found")
	}
	return rows[0], nil
}

// spaceUIDFromName extracts the uid from a "spaces/{uid}"
// resource name. Strips any trailing path segment
// (e.g. /members/foo, /invitations/foo).
func spaceUIDFromName(name string) string {
	const prefix = "spaces/"
	if len(name) <= len(prefix) || name[:len(prefix)] != prefix {
		return ""
	}
	rest := name[len(prefix):]
	if idx := strings.Index(rest, "/"); idx >= 0 {
		rest = rest[:idx]
	}
	return rest
}

// slugify turns a title into a Memos-compatible uid.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteRune('-')
		}
	}
	return b.String()
}

// currentUserID returns the current caller's record id, or
// "" if unauthenticated.
func currentUserID(ctx context.Context) string {
	u := CurrentUser(ctx)
	if u == nil {
		return ""
	}
	return u.Id
}

// intToStr avoids an import for the small int-to-string
// conversion used by ListSpaces's IN-style filter.
func intToStr(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
