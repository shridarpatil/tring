// sip.js wrapper. Hides the UserAgent/Registerer/Invitation churn behind
// a smaller surface tailored to Tring's flow: register on connect, listen
// for inbound INVITEs (which is how originated calls reach the agent),
// expose accept/hangup/DTMF on the active session.

import {
  UserAgent,
  Registerer,
  Invitation,
  Inviter,
  SessionState,
  type Session,
  type URI,
} from "sip.js";

import type { SipCredentialsResponse } from "./types";

export type SipEvents = {
  onInvitation: (i: Invitation) => void;
  onRegistered: () => void;
  onUnregistered: (reason?: string) => void;
};

export class SipAdapter {
  private ua: UserAgent | null = null;
  private registerer: Registerer | null = null;
  private remoteAudio: HTMLAudioElement | null = null;

  constructor(private readonly events: SipEvents) {}

  async connect(creds: SipCredentialsResponse): Promise<void> {
    const uri = UserAgent.makeURI(
      `sip:${creds.sip_username}@${creds.sip_domain}`,
    );
    if (!uri) {
      throw new Error("invalid SIP URI from credentials");
    }

    this.ensureRemoteAudio();

    this.ua = new UserAgent({
      uri,
      authorizationUsername: creds.sip_username,
      authorizationPassword: creds.sip_password,
      transportOptions: { server: creds.ws_url },
      // ICE servers flow from the credentials response into the
      // underlying RTCPeerConnection. With TURN configured server-side,
      // browsers behind symmetric NAT / CGNAT get a relay candidate and
      // calls connect; without TURN, those same browsers see ICE
      // failures and the call sets up but has no audio.
      sessionDescriptionHandlerFactoryOptions: {
        peerConnectionConfiguration: {
          iceServers: creds.ice_servers ?? [],
        },
      },
      delegate: {
        onInvite: (invitation: Invitation) => {
          this.wireAudio(invitation);
          this.events.onInvitation(invitation);
        },
        onDisconnect: (err) => {
          this.events.onUnregistered(err ? String(err) : "disconnected");
        },
      },
      logBuiltinEnabled: false,
    });

    await this.ua.start();
    this.registerer = new Registerer(this.ua);
    await this.registerer.register({
      requestDelegate: {
        onAccept: () => this.events.onRegistered(),
      },
    });
  }

  /** Place a SIP INVITE directly (not used by the API-driven path; kept for completeness). */
  async invite(target: URI | string): Promise<Inviter> {
    if (!this.ua) throw new Error("not connected");
    const uri = typeof target === "string" ? UserAgent.makeURI(target) : target;
    if (!uri) throw new Error("invalid target URI");
    const inv = new Inviter(this.ua, uri);
    this.wireAudio(inv);
    await inv.invite();
    return inv;
  }

  async disconnect(): Promise<void> {
    try {
      await this.registerer?.unregister();
    } catch {
      /* ignore */
    }
    await this.ua?.stop();
    this.ua = null;
    this.registerer = null;
  }

  private ensureRemoteAudio(): void {
    if (this.remoteAudio) return;
    const a = document.createElement("audio");
    a.autoplay = true;
    a.style.display = "none";
    a.setAttribute("data-tring-audio", "");
    document.body.appendChild(a);
    this.remoteAudio = a;
  }

  /**
   * Pipes the remote SDP track into a hidden <audio> element so the agent
   * actually hears the other side. sip.js doesn't do this for you.
   */
  private wireAudio(session: Session): void {
    session.stateChange.addListener((state) => {
      if (state !== SessionState.Established) return;
      const pc = (session.sessionDescriptionHandler as any)?.peerConnection;
      if (!pc) return;
      const stream = new MediaStream();
      for (const receiver of pc.getReceivers() as RTCRtpReceiver[]) {
        if (receiver.track) stream.addTrack(receiver.track);
      }
      if (this.remoteAudio) this.remoteAudio.srcObject = stream;
    });
  }
}
