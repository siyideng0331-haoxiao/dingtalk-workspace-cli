package app

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	authpkg "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/auth"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/cobracmd"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/dwsauth"
	apperrors "github.com/DingTalk-Real-AI/dingtalk-workspace-cli/internal/errors"
	"github.com/DingTalk-Real-AI/dingtalk-workspace-cli/pkg/config"
	"github.com/spf13/cobra"
)

type deapLoginDWSGrantIssuer interface {
	Issue(context.Context, string) (*dwsauth.Grant, error)
}

var (
	deapLoginDWSHTTPClient          = &http.Client{Timeout: 30 * time.Second}
	deapLoginDWSClientIDProvider    = authpkg.FetchClientIDFromMCP
	deapLoginDWSGrantClientProvider = func(configDir string) (deapLoginDWSGrantIssuer, error) {
		return dwsauth.NewClient(config.GetDEAPOpenAPIBaseURL(), deapLoginDWSHTTPClient,
			dwsDigitalEmployeeGrantOAuth{configDir: configDir})
	}
	deapLoginDWSExchangeAuthCode = func(
		ctx context.Context,
		configDir, clientID, code, corpID, uid string,
	) (*authpkg.TokenData, error) {
		provider := authpkg.NewOAuthProvider(configDir, nil)
		provider.SetMCPClientID(clientID)
		configureOAuthProviderCompatibility(provider, configDir)
		return provider.ExchangeAuthCodeForIdentity(ctx, code, corpID, uid)
	}
)

type dwsDigitalEmployeeGrantOAuth struct {
	configDir string
}

func (o dwsDigitalEmployeeGrantOAuth) AccessToken(ctx context.Context) (string, error) {
	return withOperatorRuntimeProfile(func() (string, error) {
		return ResolveAuxiliaryAccessToken(ctx, o.configDir, "")
	})
}

func (o dwsDigitalEmployeeGrantOAuth) RefreshRejectedAccessToken(
	ctx context.Context, rejected string,
) (string, error) {
	return withOperatorRuntimeProfile(func() (string, error) {
		return forceRefreshRejectedAccessToken(ctx, o.configDir, rejected)
	})
}

func withOperatorRuntimeProfile(action func() (string, error)) (string, error) {
	restore := replaceRuntimeProfile("")
	defer restore()
	return action()
}

func mountDigitalEmployeeDWSLogin(commands []*cobra.Command) []*cobra.Command {
	for _, command := range commands {
		if command == nil || command.Name() != "deap" {
			continue
		}
		manage := cobracmd.ChildByName(command, "manage")
		if manage != nil && cobracmd.ChildByName(manage, "login-dws") == nil {
			manage.AddCommand(newDeapLoginDWSCommand())
		}
		break
	}
	compatibilityRoot := cobracmd.NewHiddenGroupCommand("deep", "DEAP 兼容入口")
	compatibilityManage := cobracmd.NewHiddenGroupCommand("manage", "数字员工管理兼容入口")
	compatibilityManage.AddCommand(newDeapLoginDWSCommand())
	compatibilityRoot.AddCommand(compatibilityManage)
	return append(commands, compatibilityRoot)
}

func newDeapLoginDWSCommand() *cobra.Command {
	var assistantID, authCode, corpID, uid string
	cmd := &cobra.Command{
		Use:               "login-dws",
		Short:             "以数字员工身份登录 DWS",
		Long:              "根据 Assistant ID 获取或直接接收数字员工的一次性 DWS 授权，并将登录态写入当前 DWS_CONFIG_DIR 对应的精确身份 profile；不会替换当前操作人的精确身份 token 或兼容镜像。",
		Args:              cobra.NoArgs,
		DisableAutoGenTag: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			assistantID = strings.TrimSpace(assistantID)
			authCode = strings.TrimSpace(authCode)
			corpID = strings.TrimSpace(corpID)
			uid = strings.TrimSpace(uid)
			hasAssistantID := assistantID != ""
			hasInjectedGrant := authCode != "" || corpID != "" || uid != ""
			if hasAssistantID && hasInjectedGrant {
				return apperrors.NewValidation(
					"--assistant-id cannot be used with --auth-code, --corp-id, or --uid")
			}
			if !hasAssistantID && authCode == "" {
				return apperrors.NewValidation("one of --assistant-id or --auth-code is required")
			}
			if !hasAssistantID && (corpID == "" || uid == "") {
				return apperrors.NewValidation(
					"--corp-id and --uid are required with --auth-code")
			}
			configDir := defaultConfigDir()
			grant := &dwsauth.Grant{
				CorpID:   corpID,
				UID:      uid,
				AuthCode: authCode,
			}
			if hasAssistantID {
				grantClient, err := deapLoginDWSGrantClientProvider(configDir)
				if err != nil {
					return apperrors.NewInternal("failed to initialize digital employee DWS login")
				}

				grantCtx, cancelGrant := context.WithTimeout(cmd.Context(), 45*time.Second)
				grant, err = func() (*dwsauth.Grant, error) {
					restore := replaceRuntimeProfile("")
					defer restore()
					return grantClient.Issue(grantCtx, assistantID)
				}()
				cancelGrant()
				if err != nil {
					return apperrors.NewAuth(fmt.Sprintf(
						"failed to authorize digital employee DWS identity: %v", err))
				}
			}
			if grant == nil {
				return apperrors.NewInternal("digital employee DWS grant is empty")
			}
			grantCorpID := strings.TrimSpace(grant.CorpID)
			grantUID := strings.TrimSpace(grant.UID)
			if grantCorpID == "" || grantUID == "" || strings.TrimSpace(grant.AuthCode) == "" {
				return apperrors.NewInternal("digital employee DWS grant identity is incomplete")
			}
			exactSelector := authpkg.ProfileSelector(authpkg.Profile{
				CorpID: grantCorpID,
				UserID: grantUID,
			})
			if strings.TrimSpace(exactSelector) == "" {
				return apperrors.NewInternal("digital employee DWS identity is empty")
			}

			exchangeCtx, cancelExchange := context.WithTimeout(cmd.Context(), time.Minute)
			clientID, err := deapLoginDWSClientIDProvider(exchangeCtx)
			if err != nil {
				cancelExchange()
				return apperrors.NewAuth(fmt.Sprintf(
					"failed to resolve DWS OAuth client from MCP: %v", err))
			}
			clientID = strings.TrimSpace(clientID)
			if clientID == "" {
				cancelExchange()
				return apperrors.NewAuth("DWS OAuth client from MCP is empty")
			}
			tokenData, err := func() (*authpkg.TokenData, error) {
				restore := replaceRuntimeProfile(exactSelector)
				defer restore()
				return deapLoginDWSExchangeAuthCode(exchangeCtx, configDir,
					clientID, grant.AuthCode, grantCorpID, grantUID)
			}()
			cancelExchange()
			if err != nil {
				return apperrors.NewAuth(fmt.Sprintf(
					"failed to exchange digital employee DWS authorization: %v", err))
			}
			if tokenData == nil ||
				strings.TrimSpace(tokenData.CorpID) != grantCorpID ||
				strings.TrimSpace(tokenData.UserID) != grantUID {
				return apperrors.NewAuth("exchanged DWS identity does not match the digital employee")
			}
			ResetRuntimeTokenCache()
			clearCompatCache()

			output := cmd.OutOrStdout()
			fmt.Fprintln(output, "[OK] 数字员工 DWS 登录成功")
			if assistantID != "" {
				fmt.Fprintf(output, "%-16s%s\n", "Assistant ID:", assistantID)
			}
			fmt.Fprintf(output, "%-16s%s\n", "Profile:", exactSelector)
			fmt.Fprintf(output, "%-16s%s\n", "企业 ID:", grantCorpID)
			fmt.Fprintf(output, "%-16s%s\n", "用户:", grantUID)
			if !tokenData.ExpiresAt.IsZero() {
				fmt.Fprintf(output, "%-16s%s\n", "有效期:", authLoginFormatExpiry(tokenData.ExpiresAt))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&assistantID, "assistant-id", "", "数字员工 Assistant ID")
	cmd.Flags().StringVar(&authCode, "auth-code", "", "已获取的一次性数字员工 DWS AuthCode")
	cmd.Flags().StringVar(&corpID, "corp-id", "", "AuthCode 对应的数字员工 CorpID")
	cmd.Flags().StringVar(&uid, "uid", "", "AuthCode 对应的数字员工 UID")
	return cmd
}
