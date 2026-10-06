// Localizes the backend's canonical (Chinese) API messages for the current
// interface language. The backend keeps a single message per error; this
// table maps it to English / Khmer at the API boundary. Unmapped or dynamic
// messages pass through unchanged, so a stale entry degrades gracefully.

export type ApiLang = "km" | "en" | "zh";

interface ErrTranslation {
  en: string;
  km: string;
}

const ERR_MAP: Record<string, ErrTranslation> = {
  "网络已断开，请检查网络连接后重试": { en: "You are offline — check your connection and retry", km: "អ្នកបានដាច់ពីអ៊ីនធឺណិត — សូមពិនិត្យការតភ្ជាប់ ហើយព្យាយាមម្តងទៀត" },
  "无法连接服务器，请稍后重试": { en: "Cannot reach the server — please retry shortly", km: "មិនអាចភ្ជាប់ទៅម៉ាស៊ីនបម្រើ — សូមព្យាយាមម្តងទៀត" },
  "2FA 查询失败": { en: "2FA lookup failed", km: "ការស្វែងរក 2FA បរាជ័យ" },
  "Telegram 必须设置 bot_token": { en: "Telegram requires bot_token", km: "Telegram ត្រូវការ bot_token" },
  "rating 必须是 -1 或 1": { en: "rating must be -1 or 1", km: "ការវាយតម្លៃត្រូវតែ -1 ឬ 1" },
  "url 不能为空": { en: "URL is required", km: "URL មិនអាចទទេ" },
  "不存在": { en: "Not found", km: "រកមិនឃើញ" },
  "不存在或为系统角色": { en: "Not found or a system role", km: "រកមិនឃើញ ឬជាតួនាទីប្រព័ន្ធ" },
  "不支持的语言": { en: "Unsupported language", km: "ភាសាមិនគាំទ្រ" },
  "不支持的通知偏好": { en: "Unsupported notification preference", km: "ចំណូលចិត្តជូនដំណឹងមិនគាំទ្រ" },
  "不能禁用平台管理员账号": { en: "Platform admin accounts cannot be disabled", km: "មិនអាចបិទគណនីអ្នកគ្រប់គ្រងវេទិកា" },
  "两步验证已关闭": { en: "Two-factor authentication disabled", km: "ការផ្ទៀងផ្ទាត់ពីរជាន់បានបិទ" },
  "两步验证已启用": { en: "Two-factor authentication enabled", km: "ការផ្ទៀងផ្ទាត់ពីរជាន់បានបើក" },
  "令牌无效或已过期": { en: "Token is invalid or expired", km: "សញ្ញាសម្គាល់មិនត្រឹមត្រូវ ឬផុតកំណត់" },
  "令牌生成失败": { en: "Token generation failed", km: "ការបង្កើតសញ្ញាសម្គាល់បរាជ័យ" },
  "会话不存在": { en: "Session not found", km: "រកមិនឃើញការសន្ទនា" },
  "会话平台信息缺失": { en: "Session platform info is missing", km: "ព័ត៌មានវេទិកានៃការសន្ទនាបាត់" },
  "保存回复失败": { en: "Failed to save reply", km: "មិនអាចរក្សាទុកការឆ្លើយ" },
  "全部已读": { en: "All marked as read", km: "បានសម្គាល់ថាបានអានទាំងអស់" },
  "分配失败": { en: "Assignment failed", km: "ការចាត់ចែងបរាជ័យ" },
  "创建失败": { en: "Creation failed", km: "ការបង្កើតបរាជ័យ" },
  "删除失败": { en: "Deletion failed", km: "ការលុបបរាជ័យ" },
  "加密失败": { en: "Encryption failed", km: "ការអ៊ិនគ្រីបបរាជ័យ" },
  "原密码错误": { en: "Current password is incorrect", km: "ពាក្យសម្ងាត់បច្ចុប្បន្នមិនត្រឹមត្រូវ" },
  "名称不能为空": { en: "Name is required", km: "ឈ្មោះមិនអាចទទេ" },
  "启用平台 Webhook 时必须设置签名密钥": { en: "A signing secret is required when enabling platform webhooks", km: "ត្រូវការសោហត្ថលេខាពេលបើក webhook វេទិកា" },
  "回复内容不能为空": { en: "Reply content cannot be empty", km: "ខ្លឹមសារឆ្លើយមិនអាចទទេ" },
  "套餐已更新": { en: "Plan updated", km: "កញ្ចប់បានធ្វើបច្ចុប្បន្នភាព" },
  "媒体不存在": { en: "Media not found", km: "រកមិនឃើញមេឌៀ" },
  "客户不存在": { en: "Customer not found", km: "រកមិនឃើញអតិថិជន" },
  "密码处理失败": { en: "Password processing failed", km: "ដំណើរការពាក្យសម្ងាត់បរាជ័យ" },
  "密码已更新": { en: "Password updated", km: "ពាក្យសម្ងាត់បានធ្វើបច្ចុប្បន្នភាព" },
  "已保存": { en: "Saved", km: "បានរក្សាទុក" },
  "已停用": { en: "Deactivated", km: "បានបិទ" },
  "已分配": { en: "Assigned", km: "បានចាត់ចែង" },
  "已创建": { en: "Created", km: "បានបង្កើត" },
  "已创建租户": { en: "Tenant created", km: "អ្នកជួលត្រូវបានបង្កើត" },
  "已删除": { en: "Deleted", km: "បានលុប" },
  "已发送": { en: "Sent", km: "បានផ្ញើ" },
  "已取消": { en: "Cancelled", km: "បានបោះបង់" },
  "已忽略": { en: "Dismissed", km: "បានបោះបង់" },
  "已接受": { en: "Accepted", km: "បានទទួលយក" },
  "已接管": { en: "Taken over", km: "បានទទួលកាន់កាប់" },
  "已撤销": { en: "Revoked", km: "បានដកហូត" },
  "已更新": { en: "Updated", km: "បានធ្វើបច្ចុប្បន្នភាព" },
  "已添加": { en: "Added", km: "បានបន្ថែម" },
  "已移除": { en: "Removed", km: "បានយកចេញ" },
  "已解决": { en: "Resolved", km: "បានដោះស្រាយ" },
  "已记录": { en: "Recorded", km: "បានកត់ត្រា" },
  "已读": { en: "Marked as read", km: "បានសម្គាល់ថាបានអាន" },
  "已重新加入索引队列": { en: "Requeued for indexing", km: "បានដាក់ចូលជួរលិបិក្រមម្តងទៀត" },
  "接管失败": { en: "Takeover failed", km: "ការទទួលកាន់កាប់បរាជ័យ" },
  "文档处理失败: 无可提取的文本内容": { en: "Document processing failed: no extractable text", km: "ដំណើរការឯកសារបរាជ័យ៖ គ្មានអត្ថបទអាចស្រង់បាន" },
  "无效的 API 密钥": { en: "Invalid API key", km: "សោ API មិនត្រឹមត្រូវ" },
  "无效的会话 ID": { en: "Invalid session ID", km: "លេខសម្គាល់ការសន្ទនាមិនត្រឹមត្រូវ" },
  "无效的参数": { en: "Invalid parameters", km: "ប៉ារ៉ាម៉ែត្រមិនត្រឹមត្រូវ" },
  "无效的文档 ID": { en: "Invalid document ID", km: "លេខសម្គាល់ឯកសារមិនត្រឹមត្រូវ" },
  "无效的消息 ID": { en: "Invalid message ID", km: "លេខសម្គាល់សារមិនត្រឹមត្រូវ" },
  "无更新字段": { en: "No fields to update", km: "គ្មានវាលត្រូវធ្វើបច្ចុប្បន្នភាព" },
  "更新失败": { en: "Update failed", km: "ការធ្វើបច្ចុប្បន្នភាពបរាជ័យ" },
  "更新标签失败": { en: "Failed to update tags", km: "មិនអាចធ្វើបច្ចុប្បន្នភាពស្លាក" },
  "月度文档配额已用尽，请升级套餐": { en: "Monthly document quota exhausted — please upgrade your plan", km: "កូតាឯកសារប្រចាំខែអស់ហើយ — សូមដំឡើងកញ្ចប់" },
  "未启用两步验证": { en: "Two-factor authentication is not enabled", km: "ការផ្ទៀងផ្ទាត់ពីរជាន់មិនទាន់បើក" },
  "未提供认证令牌": { en: "No authentication token provided", km: "មិនបានផ្តល់សញ្ញាសម្គាល់ផ្ទៀងផ្ទាត់" },
  "未设置 API Key": { en: "API key is not set", km: "សោ API មិនបានកំណត់" },
  "查询失败": { en: "Query failed", km: "ការស្វែងរកបរាជ័យ" },
  "标签已更新": { en: "Tags updated", km: "ស្លាកបានធ្វើបច្ចុប្បន្នភាព" },
  "标题不能为空": { en: "Title is required", km: "ចំណងជើងមិនអាចទទេ" },
  "标题和内容不能为空": { en: "Title and content are required", km: "ចំណងជើង និងខ្លឹមសារមិនអាចទទេ" },
  "模型连接测试失败": { en: "Model connection test failed", km: "ការសាកល្បងការតភ្ជាប់ម៉ូដែលបរាជ័យ" },
  "注册失败": { en: "Registration failed", km: "ការចុះឈ្មោះបរាជ័យ" },
  "注册已关闭，请联系管理员": { en: "Registration is closed — contact your administrator", km: "ការចុះឈ្មោះបានបិទ — សូមទាក់ទងអ្នកគ្រប់គ្រង" },
  "消息不存在": { en: "Message not found", km: "រកមិនឃើញសារ" },
  "添加失败": { en: "Failed to add", km: "ការបន្ថែមបរាជ័យ" },
  "状态已更新": { en: "Status updated", km: "ស្ថានភាពបានធ្វើបច្ចុប្បន្នភាព" },
  "生成回答失败": { en: "Failed to generate an answer", km: "មិនអាចបង្កើតចម្លើយ" },
  "生成摘要失败": { en: "Failed to generate a summary", km: "មិនអាចបង្កើតសេចក្តីសង្ខេប" },
  "用户不存在": { en: "User not found", km: "រកមិនឃើញអ្នកប្រើ" },
  "用户名或密码错误": { en: "Incorrect username or password", km: "ឈ្មោះអ្នកប្រើ ឬពាក្យសម្ងាត់មិនត្រឹមត្រូវ" },
  "用户名或邮箱已存在": { en: "Username or email already exists", km: "ឈ្មោះអ្នកប្រើ ឬអ៊ីមែលមានរួចហើយ" },
  "用户查询失败": { en: "User lookup failed", km: "ការស្វែងរកអ្នកប្រើបរាជ័យ" },
  "登录尝试过多，账号已锁定，请稍后再试": { en: "Too many login attempts — account locked, try again later", km: "ការព្យាយាមចូលច្រើនពេក — គណនីត្រូវបានចាក់សោ សូមព្យាយាមម្តងទៀតពេលក្រោយ" },
  "缺少或无效的文件 (field name: file)": { en: "Missing or invalid file (field name: file)", km: "ឯកសារបាត់ ឬមិនត្រឹមត្រូវ (field name: file)" },
  "获取模型列表失败": { en: "Failed to fetch the model list", km: "មិនអាចទាញយកបញ្ជីម៉ូដែល" },
  "营业时间已保存": { en: "Business hours saved", km: "ម៉ោងធ្វើការបានរក្សាទុក" },
  "角色不存在": { en: "Role not found", km: "រកមិនឃើញតួនាទី" },
  "超级管理员账号不可禁用": { en: "Super admin accounts cannot be disabled", km: "គណនីអ្នកគ្រប់គ្រងជាន់ខ្ពស់មិនអាចបិទបានទេ" },
  "认证格式错误": { en: "Invalid authentication format", km: "ទម្រង់ផ្ទៀងផ្ទាត់មិនត្រឹមត្រូវ" },
  "该平台账号已连接到其他客户": { en: "This platform account is already connected to another customer", km: "គណនីវេទិកានេះបានភ្ជាប់ទៅអតិថិជនផ្សេងរួចហើយ" },
  "该平台需要配置 access_token": { en: "This platform requires an access_token", km: "វេទិកានេះត្រូវការ access_token" },
  "该平台需要配置平台集成 ID (page_id 或 instagram_business_id)": { en: "This platform requires an integration ID (page_id or instagram_business_id)", km: "វេទិកានេះត្រូវការលេខសម្គាល់រួមបញ្ចូល (page_id ឬ instagram_business_id)" },
  "该用户已在客服团队中": { en: "This user is already on the agent team", km: "អ្នកប្រើនេះមាននៅក្នុងក្រុមភ្នាក់ងាររួចហើយ" },
  "请使用邀请链接添加客服": { en: "Use an invite link to add agents", km: "សូមប្រើតំណអញ្ជើញដើម្បីបន្ថែមភ្នាក់ងារ" },
  "只有租户所有者可以邀请客服": { en: "Only the tenant owner can invite agents", km: "មានតែម្ចាស់ហាងទេដែលអាចអញ្ជើញភ្នាក់ងារ" },
  "只有租户所有者可以查看邀请": { en: "Only the tenant owner can view invites", km: "មានតែម្ចាស់ហាងទេដែលអាចមើលការអញ្ជើញ" },
  "只有租户所有者可以撤销邀请": { en: "Only the tenant owner can revoke invites", km: "មានតែម្ចាស់ហាងទេដែលអាចដកហូតការអញ្ជើញ" },
  "客服称呼过长": { en: "The agent name is too long", km: "ឈ្មោះភ្នាក់ងារវែងពេក" },
  "技能标签过多": { en: "Too many skill tags", km: "ស្លាកជំនាញច្រើនពេក" },
  "生成邀请失败": { en: "Failed to create the invite", km: "ការបង្កើតការអញ្ជើញបរាជ័យ" },
  "撤销失败": { en: "Failed to revoke", km: "ការដកហូតបរាជ័យ" },
  "邀请不存在或已被撤销": { en: "Invite not found or already revoked", km: "រកមិនឃើញការអញ្ជើញ ឬបានដកហូតរួចហើយ" },
  "有效期设置无效": { en: "Invalid validity period (1 hour to 90 days)", km: "រយៈពេលមានសុពលភាពមិនត្រឹមត្រូវ (1 ម៉ោង ដល់ 90 ថ្ងៃ)" },
  "可加入人数无效": { en: "Invalid number of people that can join", km: "ចំនួនអ្នកអាចចូលរួមមិនត្រឹមត្រូវ" },
  "可加入人数超过剩余席位": { en: "More people than seats left", km: "ចំនួនអ្នកចូលរួមលើសពីកន្លែងទំនេរ" },
  "邀请链接已用完": { en: "This invite link has been used up", km: "តំណអញ្ជើញនេះប្រើអស់ហើយ" },
  "只有租户所有者可以查看邀请历史": { en: "Only the tenant owner can view the invite history", km: "មានតែម្ចាស់ហាងទេដែលអាចមើលប្រវត្តិការអញ្ជើញ" },
  "邀请链接无效、已被使用或已过期": { en: "This invite link is invalid, already used, or expired", km: "តំណអញ្ជើញនេះមិនត្រឹមត្រូវ បានប្រើរួច ឬផុតកំណត់" },
  "平台管理员账号不能作为客服加入": { en: "Platform admin accounts cannot join as agents", km: "គណនីអ្នកគ្រប់គ្រងវេទិកាមិនអាចចូលជាភ្នាក់ងារបានទេ" },
  "不能接受自己的邀请": { en: "You cannot accept your own invite", km: "អ្នកមិនអាចទទួលយកការអញ្ជើញរបស់ខ្លួនឯងបានទេ" },
  "该账号是独立的商家账号，不能作为客服加入": { en: "This account is an independent merchant and cannot join as an agent", km: "គណនីនេះជាហាងឯករាជ្យ មិនអាចចូលជាភ្នាក់ងារបានទេ" },
  "该账号已在客服团队中": { en: "This account is already on an agent team", km: "គណនីនេះស្ថិតនៅក្រុមភ្នាក់ងាររួចហើយ" },
  "加入失败": { en: "Failed to join", km: "ការចូលរួមបរាជ័យ" },
  "只有平台管理员可以启用或停用账号": { en: "Only platform admins can enable or disable accounts", km: "មានតែអ្នកគ្រប់គ្រងវេទិកាទេដែលអាចបើក/បិទគណនី" },
  "该配置未设置 API Key": { en: "This configuration has no API key set", km: "ការកំណត់នេះមិនបានកំណត់សោ API" },
  "语音转写失败": { en: "Voice transcription failed", km: "ការបំលែងសំឡេងបរាជ័យ" },
  "请保存此密钥,只显示一次": { en: "Save this secret — it is shown only once", km: "រក្សាទុកសោនេះ — បង្ហាញតែម្តង" },
  "请先调用 setup": { en: "Call setup first", km: "សូមហៅ setup ជាមុនសិន" },
  "请求格式错误": { en: "Invalid request format", km: "ទម្រង់សំណើមិនត្រឹមត្រូវ" },
  "请求格式错误或新密码过短 (最少 6 位)": { en: "Invalid request or new password too short (min 6 characters)", km: "សំណើមិនត្រឹមត្រូវ ឬពាក្យសម្ងាត់ថ្មីខ្លីពេក (យ៉ាងតិច 6 តួ)" },
  "请求过于频繁": { en: "Too many requests", km: "សំណើច្រើនពេក" },
  "读取文件失败": { en: "Failed to read the file", km: "មិនអាចអានឯកសារ" },
  "读取音频失败": { en: "Failed to read the audio", km: "មិនអាចអានសំឡេង" },
  "账户已被禁用": { en: "Account has been disabled", km: "គណនីត្រូវបានបិទ" },
  "跨域请求被拒绝": { en: "Cross-origin request rejected", km: "សំណើឆ្លងដែនត្រូវបានបដិសេធ" },
  "邀请码无效": { en: "Invalid invite code", km: "លេខកូដអញ្ជើញមិនត្រឹមត្រូវ" },
  "限流服务不可用": { en: "Rate limiting service unavailable", km: "សេវាកំណត់អត្រាមិនអាចប្រើបាន" },
  "需要两步验证码": { en: "Two-factor code required", km: "ត្រូវការលេខកូដពីរជាន់" },
  "需要平台管理员权限": { en: "Platform admin permission required", km: "ត្រូវការសិទ្ធិអ្នកគ្រប់គ្រងវេទិកា" },
  "需要租户管理员权限": { en: "Tenant owner permission required", km: "ត្រូវការសិទ្ធិម្ចាស់ហាង" },
  "页面无可提取的正文文本 (可能是 JS 渲染的单页应用, 请改用粘贴文本)": { en: "No extractable text on the page (may be a JS-rendered SPA — paste the text instead)", km: "គ្មានអត្ថបទអាចស្រង់បានលើទំព័រ (អាចជា SPA បង្ហាញដោយ JS — សូមបិទភ្ជាប់អត្ថបទជំនួសវិញ)" },
  "验证码错误": { en: "Incorrect verification code", km: "លេខកូដផ្ទៀងផ្ទាត់មិនត្រឹមត្រូវ" },
  "登录失败": { en: "Login failed", km: "ការចូលបរាជ័យ" },
  "请求失败": { en: "Request failed", km: "សំណើបរាជ័យ" },
};

// Dynamic-prefix fallbacks (backend concatenates details onto these).
const PREFIX_MAP: [string, string, string][] = [
  ["文档处理失败: ", "Document processing failed: ", "ដំណើរការឯកសារបរាជ័យ៖ "],
];

/** Map a message using the persisted interface language (localStorage). */
export function localizeCurrentLang(message: string): string {
  const lang = typeof window !== "undefined" ? localStorage.getItem("lang") : null;
  return localizeApiMessage(message, lang);
}

/** Map a backend message to the given interface language. */
export function localizeApiMessage(message: string, lang: string | null): string {
  if (!lang || lang === "zh" || lang === "auto") return message;
  const entry = ERR_MAP[message];
  if (entry) return lang === "km" ? entry.km : entry.en;
  for (const [zhPrefix, enPrefix, kmPrefix] of PREFIX_MAP) {
    if (message.startsWith(zhPrefix)) {
      const detail = message.slice(zhPrefix.length);
      return (lang === "km" ? kmPrefix : enPrefix) + detail;
    }
  }
  return message;
}
