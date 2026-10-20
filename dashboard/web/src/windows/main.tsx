import { createRoot } from 'react-dom/client'
import WindowsApp from './WindowsApp'
import './windows.css'
import './ui-font.css'
import './dialogue-font.css'

const root = document.getElementById('root')
if (!root) throw new Error('Missing Windows app root element')

createRoot(root).render(<WindowsApp />)
