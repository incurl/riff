import { toast } from "react-hot-toast";
import { Button } from "@/components/ui/button";
import { absolutifyLink } from "@/lib/browser";
import { handleError } from "@/lib/error";
import { ROUTES } from "@/router/routes";
import { IdentityProvider, IdentityProvider_Type } from "@/types/proto/api/v1/idp_service_pb";
import { withBasePath } from "@/utils/base-path";
import { useTranslate } from "@/utils/i18n";
import { storeOAuthState } from "@/utils/oauth";

interface Props {
  identityProviderList: IdentityProvider[];
  redirectTarget?: string;
}

const IdentityProviderButtons = ({ identityProviderList, redirectTarget }: Props) => {
  const t = useTranslate();

  const handleSignInWithIdentityProvider = async (identityProvider: IdentityProvider) => {
    if (identityProvider.type === IdentityProvider_Type.OAUTH2) {
      // The OAuth provider round-trips `redirect_uri` to the browser, so the
      // path-absolute `ROUTES.AUTH_CALLBACK` must include the reverse-proxy
      // mount prefix (e.g. "/community/auth/callback") — path-absolute URLs
      // resolve against the origin, not against the Vite `<base href>`, so
      // `absolutifyLink` alone would drop the prefix under a sub-path build.
      const redirectUri = absolutifyLink(withBasePath(ROUTES.AUTH_CALLBACK));
      const oauth2Config = identityProvider.config?.config?.case === "oauth2Config" ? identityProvider.config.config.value : undefined;
      if (!oauth2Config) {
        toast.error("Identity provider configuration is invalid.");
        return;
      }

      try {
        // Generate and store secure state parameter with CSRF protection
        // Also generate PKCE parameters (code_challenge) for enhanced security if available
        const { state, codeChallenge } = await storeOAuthState(identityProvider.name, "signin", redirectTarget);

        // Build OAuth authorization URL with secure state
        // Include PKCE if available (requires HTTPS/localhost for crypto.subtle)
        // Using S256 (SHA-256) as the code_challenge_method per RFC 7636
        let authUrl = `${oauth2Config.authUrl}?client_id=${
          oauth2Config.clientId
        }&redirect_uri=${encodeURIComponent(redirectUri)}&state=${state}&response_type=code&scope=${encodeURIComponent(
          oauth2Config.scopes.join(" "),
        )}`;

        // Add PKCE parameters if available
        if (codeChallenge) {
          authUrl += `&code_challenge=${codeChallenge}&code_challenge_method=S256`;
        }

        window.location.href = authUrl;
      } catch (error) {
        handleError(error, toast.error, {
          context: "Failed to initiate OAuth flow",
          fallbackMessage: "Failed to initiate sign-in. Please try again.",
        });
      }
    }
  };

  return (
    <div className="flex w-full flex-col gap-2">
      {identityProviderList.map((identityProvider) => (
        <Button key={identityProvider.name} variant="outline" onClick={() => handleSignInWithIdentityProvider(identityProvider)}>
          {t("auth.continue-with", { provider: identityProvider.title })}
        </Button>
      ))}
    </div>
  );
};

export default IdentityProviderButtons;
