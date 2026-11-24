import './globals.css';
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
        {children}
      </body>
    </html>
  );
}
