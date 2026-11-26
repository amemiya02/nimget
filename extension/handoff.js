import { ExtensionRequestError, requestExtension } from "./pairing.js";

export const manualHandoffPrefix = "manualHandoff:";
export const manualHandoffResult = "manualHandoffResult";

export async function submitHandoff(request, credential) {
  return (await submitHandoffWithReceipt(request, credential)).taskId;
}

export function submitHandoffWithReceipt(request, credential) {
  return requestReceipt("handoffs", request, credential, [200, 201]);
}

export function lookupHandoffReceipt(handoffId, credential) {
  return requestReceipt("handoffs/status", { handoffId }, credential, [200]);
}

// Only a validated receipt confirms desktop acceptance. A missing reply can
// leave the task queued, so distinguish uncertainty from an explicit refusal.
export async function deliverHandoff(request, credential, { lookupOnly = false } = {}) {
  try {
    const receipt = lookupOnly
      ? await lookupHandoffReceipt(request.handoffId, credential)
      : await submitHandoffWithReceipt(request, credential);
    return { status: "accepted", receipt };
  } catch (error) {
    switch (error.code) {
      case "EXTENSION_CREDENTIAL_REQUIRED":
      case "PAIRING_REQUIRED":
        return { status: "rejected", reason: "pairing" };
      case "UNSUPPORTED_LINK":
        return { status: "rejected", reason: "unsupported" };
      case "HANDOFF_CONFLICT":
        return { status: "rejected", reason: "conflict" };
      case "HANDOFF_NOT_FOUND":
        return { status: "rejected", reason: "notFound" };
      case "INVALID_WEBSITE_SESSION":
        return { status: "rejected", reason: "invalidSession" };
      case "FORBIDDEN_ORIGIN":
      case "HANDOFF_REJECTED":
      case "INVALID_HANDOFF_ID":
      case "INVALID_REQUEST":
        return { status: "rejected", reason: "rejected" };
      case "DESKTOP_UNREACHABLE":
        return { status: "unconfirmed", reason: "unreachable" };
      case "HANDOFF_STORAGE_FAILED":
        return { status: "unconfirmed", reason: "storage" };
      default:
        return { status: "unconfirmed", reason: error instanceof ExtensionRequestError ? "unknown" : "local" };
    }
  }
}

export async function sha256Hex(value) {
  const hash = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(value));
  return Array.from(new Uint8Array(hash), (byte) => byte.toString(16).padStart(2, "0")).join("");
}

export async function updateHandoffBadge(result, badge) {
  await chrome.action.setBadgeBackgroundColor({ color: badge === "✓" ? "#25723B" : "#A54025" });
  await chrome.action.setBadgeText({ text: badge });
  await chrome.action.setTitle({ title: chrome.i18n.getMessage(result) });
}

async function requestReceipt(path, request, credential, statuses) {
  const result = await requestExtension(path, request, credential, statuses);
  if (typeof result.taskId !== "string" || !result.taskId.trim()) {
    throw new ExtensionRequestError("HANDOFF_UNCONFIRMED");
  }
  return result;
}

export function supportsHandoff(link) {
  try {
    const url = new URL(link);
    return (url.protocol === "http:" || url.protocol === "https:") && !url.username && !url.password;
  } catch {
    return false;
  }
}
