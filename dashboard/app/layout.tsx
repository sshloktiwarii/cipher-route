import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: "MACR Platform Gateway Telemetry",
  description: "Real-time context router monitoring, semantic caching, and fallback metrics",
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <body className="bg-slate-950 text-slate-100 min-h-screen antialiased">
        {children}
      </body>
    </html>
  );
}
