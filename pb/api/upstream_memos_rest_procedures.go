package api

// This file re-exports the generated Connect procedure names from
// the apiv1connect package under short, route-table-friendly
// names. The names match the Memos service.RPC pair: every
// constant is `memosConnect<Service><RPC>Procedure` so the route
// table reads naturally and a future rename of a proto RPC only
// has to be updated here, not in every row of the table.

import (
	"github.com/incurl/riff/proto/gen/api/v1/apiv1connect"
)

// auth_service
const (
	memosConnectAuthSignInProcedure         = apiv1connect.AuthServiceSignInProcedure
	memosConnectAuthSignOutProcedure        = apiv1connect.AuthServiceSignOutProcedure
	memosConnectAuthRefreshProcedure        = apiv1connect.AuthServiceRefreshTokenProcedure
	memosConnectAuthGetCurrentUserProcedure = apiv1connect.AuthServiceGetCurrentUserProcedure
)

// user_service
const (
	memosConnectUserCreateUserProcedure                  = apiv1connect.UserServiceCreateUserProcedure
	memosConnectUserGetUserProcedure                     = apiv1connect.UserServiceGetUserProcedure
	memosConnectUserBatchGetUsersProcedure               = apiv1connect.UserServiceBatchGetUsersProcedure
	memosConnectUserGetUserSettingProcedure              = apiv1connect.UserServiceGetUserSettingProcedure
	memosConnectUserUpdateUserSettingProcedure           = apiv1connect.UserServiceUpdateUserSettingProcedure
	memosConnectUserGetUserStatsProcedure                = apiv1connect.UserServiceGetUserStatsProcedure
	memosConnectUserListAllUserStatsProcedure            = apiv1connect.UserServiceListAllUserStatsProcedure
	memosConnectUserExportMemosProcedure                 = apiv1connect.UserServiceExportMemosProcedure
	memosConnectUserImportMemosProcedure                 = apiv1connect.UserServiceImportMemosProcedure
	memosConnectUserCreateLinkedIdentityProcedure        = apiv1connect.UserServiceCreateLinkedIdentityProcedure
	memosConnectUserDeleteLinkedIdentityProcedure        = apiv1connect.UserServiceDeleteLinkedIdentityProcedure
	memosConnectUserCreatePersonalAccessTokenProcedure   = apiv1connect.UserServiceCreatePersonalAccessTokenProcedure
	memosConnectUserDeletePersonalAccessTokenProcedure   = apiv1connect.UserServiceDeletePersonalAccessTokenProcedure
	memosConnectUserGetUserWebhookSigningSecretProcedure = apiv1connect.UserServiceGetUserWebhookSigningSecretProcedure
)

// memo_service
const (
	memosConnectMemoListMemosProcedure            = apiv1connect.MemoServiceListMemosProcedure
	memosConnectMemoGetMemoProcedure              = apiv1connect.MemoServiceGetMemoProcedure
	memosConnectMemoCreateMemoProcedure           = apiv1connect.MemoServiceCreateMemoProcedure
	memosConnectMemoUpdateMemoProcedure           = apiv1connect.MemoServiceUpdateMemoProcedure
	memosConnectMemoDeleteMemoProcedure           = apiv1connect.MemoServiceDeleteMemoProcedure
	memosConnectMemoListMemoAttachmentsProcedure  = apiv1connect.MemoServiceListMemoAttachmentsProcedure
	memosConnectMemoSetMemoAttachmentsProcedure   = apiv1connect.MemoServiceSetMemoAttachmentsProcedure
	memosConnectMemoListMemoRelationsProcedure    = apiv1connect.MemoServiceListMemoRelationsProcedure
	memosConnectMemoSetMemoRelationsProcedure     = apiv1connect.MemoServiceSetMemoRelationsProcedure
	memosConnectMemoListMemoCommentsProcedure     = apiv1connect.MemoServiceListMemoCommentsProcedure
	memosConnectMemoCreateMemoCommentProcedure    = apiv1connect.MemoServiceCreateMemoCommentProcedure
	memosConnectMemoListMemoReactionsProcedure    = apiv1connect.MemoServiceListMemoReactionsProcedure
	memosConnectMemoUpsertMemoReactionProcedure   = apiv1connect.MemoServiceUpsertMemoReactionProcedure
	memosConnectMemoDeleteMemoReactionProcedure   = apiv1connect.MemoServiceDeleteMemoReactionProcedure
	memosConnectMemoListMemoSharesProcedure       = apiv1connect.MemoServiceListMemoSharesProcedure
	memosConnectMemoCreateMemoShareProcedure      = apiv1connect.MemoServiceCreateMemoShareProcedure
	memosConnectMemoDeleteMemoShareProcedure      = apiv1connect.MemoServiceDeleteMemoShareProcedure
	memosConnectMemoGetSharedMemoProcedure        = apiv1connect.MemoServiceGetSharedMemoProcedure
	memosConnectMemoGetLinkMetadataProcedure      = apiv1connect.MemoServiceGetLinkMetadataProcedure
	memosConnectMemoBatchGetLinkMetadataProcedure = apiv1connect.MemoServiceBatchGetLinkMetadataProcedure
)

// space_service
const (
	memosConnectSpaceCreateSpaceProcedure            = apiv1connect.SpaceServiceCreateSpaceProcedure
	memosConnectSpaceUpdateSpaceProcedure            = apiv1connect.SpaceServiceUpdateSpaceProcedure
	memosConnectSpaceDeleteSpaceProcedure            = apiv1connect.SpaceServiceDeleteSpaceProcedure
	memosConnectSpaceListSpaceMembersProcedure       = apiv1connect.SpaceServiceListSpaceMembersProcedure
	memosConnectSpaceGetSpaceMemberProcedure         = apiv1connect.SpaceServiceGetSpaceMemberProcedure
	memosConnectSpaceUpdateSpaceMemberProcedure      = apiv1connect.SpaceServiceUpdateSpaceMemberProcedure
	memosConnectSpaceDeleteSpaceMemberProcedure      = apiv1connect.SpaceServiceDeleteSpaceMemberProcedure
	memosConnectSpaceCreateSpaceInvitationProcedure  = apiv1connect.SpaceServiceCreateSpaceInvitationProcedure
	memosConnectSpaceDeleteSpaceInvitationProcedure  = apiv1connect.SpaceServiceDeleteSpaceInvitationProcedure
	memosConnectSpaceAcceptSpaceInvitationProcedure  = apiv1connect.SpaceServiceAcceptSpaceInvitationProcedure
	memosConnectSpaceDeclineSpaceInvitationProcedure = apiv1connect.SpaceServiceDeclineSpaceInvitationProcedure
)

// attachment_service
const (
	memosConnectAttachmentCreateAttachmentProcedure       = apiv1connect.AttachmentServiceCreateAttachmentProcedure
	memosConnectAttachmentGetAttachmentProcedure          = apiv1connect.AttachmentServiceGetAttachmentProcedure
	memosConnectAttachmentListAttachmentsProcedure        = apiv1connect.AttachmentServiceListAttachmentsProcedure
	memosConnectAttachmentUpdateAttachmentProcedure       = apiv1connect.AttachmentServiceUpdateAttachmentProcedure
	memosConnectAttachmentDeleteAttachmentProcedure       = apiv1connect.AttachmentServiceDeleteAttachmentProcedure
	memosConnectAttachmentUploadAttachmentProcedure       = apiv1connect.AttachmentServiceUploadAttachmentProcedure
	memosConnectAttachmentBatchDeleteAttachmentsProcedure = apiv1connect.AttachmentServiceBatchDeleteAttachmentsProcedure
)

// instance_service
const (
	memosConnectInstanceGetInstanceProfileProcedure       = apiv1connect.InstanceServiceGetInstanceProfileProcedure
	memosConnectInstanceGetInstanceStatsProcedure         = apiv1connect.InstanceServiceGetInstanceStatsProcedure
	memosConnectInstanceGetInstanceSettingProcedure       = apiv1connect.InstanceServiceGetInstanceSettingProcedure
	memosConnectInstanceBatchGetInstanceSettingsProcedure = apiv1connect.InstanceServiceBatchGetInstanceSettingsProcedure
	memosConnectInstanceUpdateInstanceSettingProcedure    = apiv1connect.InstanceServiceUpdateInstanceSettingProcedure
	memosConnectInstanceTestInstanceEmailSettingProcedure = apiv1connect.InstanceServiceTestInstanceEmailSettingProcedure
)

// idp_service
const (
	memosConnectIdentityProviderListIdentityProvidersProcedure  = apiv1connect.IdentityProviderServiceListIdentityProvidersProcedure
	memosConnectIdentityProviderGetIdentityProviderProcedure    = apiv1connect.IdentityProviderServiceGetIdentityProviderProcedure
	memosConnectIdentityProviderCreateIdentityProviderProcedure = apiv1connect.IdentityProviderServiceCreateIdentityProviderProcedure
	memosConnectIdentityProviderUpdateIdentityProviderProcedure = apiv1connect.IdentityProviderServiceUpdateIdentityProviderProcedure
	memosConnectIdentityProviderDeleteIdentityProviderProcedure = apiv1connect.IdentityProviderServiceDeleteIdentityProviderProcedure
)

// ai_service
const (
	memosConnectAITranscribeProcedure = apiv1connect.AIServiceTranscribeProcedure
)
