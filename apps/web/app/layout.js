import './globals.css';
import SessionBadge from './SessionBadge.js';
import SwRegister from './SwRegister.js';

export const metadata = {
  title: 'AegisMesh',
  description: 'Field reporting and command center for disaster response',
  manifest: '/manifest.webmanifest',
};

export const viewport = { themeColor: '#0f172a', width: 'device-width', initialScale: 1 };

export default function RootLayout({ children }) {
  return (
    <html lang="en">
      <body>
        <SwRegister />
        <SessionBadge />
        {children}
      </body>
    </html>
  );
}
